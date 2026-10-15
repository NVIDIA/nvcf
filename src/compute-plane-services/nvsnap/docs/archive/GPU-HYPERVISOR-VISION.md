# GPU Hypervisor: Open-Source GPU Virtualization Layer

**Status**: Vision document / product exploration
**Date**: 2026-03-08
**Author**: NvSnap team

## Executive Summary

A thin software layer between CUDA userspace libraries and the NVIDIA kernel driver that provides complete control over GPU resources per-process — without modifying the driver or the application. No hardware partitioning (MIG), no driver patches, no firmware changes. Pure software interposition.

This does not exist as open-source software today. The cloud providers want it internally for GPU scheduling efficiency. Research systems (gVirtuS, rCUDA, AntMan, Gandiva) have explored pieces of it. Nobody has shipped the full stack.

NvSnap's checkpoint/restore work is building 80% of the userspace layer already. This document explores what a full GPU hypervisor looks like, what it enables, and whether to build it as an extension of NvSnap or as a standalone product.

## The Problem

GPUs are the most expensive resource in modern infrastructure. A single H100 costs ~$30K. Cloud GPU instances cost $2-4/hour. Yet GPU utilization in production is typically **30-50%** because:

1. **No preemption**: Once a pod gets a GPU, it holds it until termination. No time-slicing, no suspension.
2. **No oversubscription**: You can't run a 40GB model on a 24GB GPU with transparent paging.
3. **No live migration**: Can't move a GPU workload between nodes without killing it.
4. **No fine-grained isolation**: MIG gives fixed partitions (1/2, 1/4, 1/7 of GPU). Real workloads need dynamic, arbitrary slices.
5. **No observability**: `nvidia-smi` gives per-device stats. There's no per-process kernel-level GPU profiling without CUPTI instrumentation in the application.

A GPU hypervisor solves all five.

## Architecture

```
Application (PyTorch, vLLM, TensorFlow, etc.)
    |
    |  (unchanged — no application modifications)
    |
libcuda.so / libcudart.so / libnccl.so / libcublas.so
    |
    |  (unchanged — stock NVIDIA libraries)
    |
+============================================+
|          libnvsnap_gpu.so                    |
|          THE GPU HYPERVISOR                |
|                                            |
|  Layer 1: LD_PRELOAD Interception          |
|  ---------------------------------------- |
|  cudaMalloc / cudaFree      Memory track  |
|  cudaLaunchKernel           Kernel trace  |
|  cudaStreamCreate/Destroy   Stream track  |
|  cudaEventCreate/Destroy    Event track   |
|  cuMemAlloc / cuMemFree     Driver alloc  |
|  cuLaunchKernel             Driver launch |
|  ncclCommInitRank           NCCL track    |
|  ncclAllReduce / Broadcast  Collective op |
|  cublasCreate / cudnnCreate Handle track  |
|  dlopen / dlsym             Lib load hook |
|                                            |
|  Layer 2: ioctl Observation (eBPF)         |
|  ---------------------------------------- |
|  nvidia_ioctl               Command trace |
|  nv_ioctl                   UVM faults    |
|  uvm_ioctl                  Page migrate  |
|  GPU page table reads       Memory map    |
|  GPFIFO inspection          Cmd buffers   |
|                                            |
|  Layer 3: Resource Control                 |
|  ---------------------------------------- |
|  VA space management        cuMemVMM API  |
|  Memory quota enforcement   Per-process   |
|  Stream priority control    Scheduling    |
|  Host memory swap           Oversubscribe |
|                                            |
+============================================+
    |
    |  ioctl() calls to /dev/nvidia*
    |
nvidia.ko (kernel driver, unmodified)
    |
GPU hardware
```

### Key Design Principle

The hypervisor sits entirely in **userspace**. It does not modify nvidia.ko. It does not require custom kernel modules. It loads via `LD_PRELOAD` and attaches eBPF probes at runtime. This means:

- Works with any NVIDIA driver version (adaptors per version for ioctl decoding)
- Deployable as a DaemonSet in Kubernetes (just set LD_PRELOAD on GPU pods)
- No host privilege escalation beyond `CAP_BPF` for eBPF probes
- Can be enabled/disabled per-pod without node changes

## Capabilities

### 1. GPU Checkpoint/Restore (NvSnap Core — Exists Today)

What we already have for single-GPU, and are building for multi-GPU (#25):

- Track all GPU memory allocations via `cudaMalloc`/`cuMemAlloc` interception
- Save GPU memory to host (D2H copy) before CRIU process checkpoint
- Restore GPU memory at original virtual addresses via CUDA VMM API (`cuMemAddressReserve` + `cuMemMap`)
- Reconstruct NCCL communicators across ranks
- Transparent to the application

**Status**: Single-GPU works. Multi-GPU in progress.

### 2. Live GPU Migration

Checkpoint on node A, restore on node B, on a different physical GPU.

```
Node A (H100 #3)                    Node B (H100 #7)
+------------------+                +------------------+
| vLLM serving     |                | empty GPU        |
| 70B model, TP=4  |                |                  |
+------------------+                +------------------+
        |                                   ^
        | 1. Quiesce (drain ops)            |
        | 2. Save GPU mem to host           |
        | 3. CRIU dump process              |
        | 4. Transfer checkpoint            |
        |     (process image + GPU data)    |
        +----------------------------------->
                                    | 5. CRIU restore process
                                    | 6. Reserve GPU VAs
                                    | 7. Restore GPU memory (H2D)
                                    | 8. Rebuild NCCL comms
                                    | 9. Resume inference
```

**What the hypervisor adds beyond checkpoint/restore:**

- **Device ordinal remapping**: Source had GPU 3, destination has GPU 7. The hypervisor intercepts `cudaSetDevice()` and remaps transparently. The application still thinks it's on device 3.
- **Topology-aware NCCL reconstruction**: Different NVLink/NVSwitch topology on the destination node. NCCL communicators are rebuilt with the new topology, not replayed from the old one.
- **Memory capacity mismatch handling**: Source had 80GB H100, destination has 80GB H100. Same capacity — straightforward. But if destination has 40GB A100, the hypervisor could page some tensors to host RAM transparently (see oversubscription below).

**Why this matters**: Kubernetes GPU scheduling today is "pin a pod to a GPU until it dies." Live migration enables:
- **GPU defragmentation**: Consolidate workloads onto fewer nodes, free up GPUs for new jobs
- **Maintenance without downtime**: Drain a GPU node for driver updates without killing serving workloads
- **Cost optimization**: Move batch training to spot instances, migrate back to on-demand when preempted

**Effort beyond NvSnap checkpoint/restore**: ~4-8 weeks. The hard parts are device ordinal remapping (moderate — intercept all device-specific calls) and network transfer of GPU memory (large data, need streaming to overlap with CRIU image transfer).

### 3. GPU Memory Oversubscription

Run workloads that need more GPU memory than physically available by transparently paging cold GPU memory to host RAM.

```
Application thinks it has 80GB GPU memory
                |
+----------------------------------+
|  GPU Hypervisor Memory Manager   |
|                                  |
|  Hot set (on GPU): 60GB          |  ← frequently accessed
|  Cold set (on host): 30GB        |  ← not accessed recently
|  Total virtual: 90GB             |
|                                  |
|  Page fault handler:             |
|    access cold page → evict LRU  |
|    from GPU, swap in requested   |
+----------------------------------+
                |
        Physical GPU: 80GB
        Host RAM buffer: 30GB
```

**How it works:**

1. **Allocation interception**: `cudaMalloc(90GB)` on an 80GB GPU. The hypervisor allocates 80GB on GPU + 10GB on host. Returns a virtual pointer that spans both.

2. **Access tracking**: Use CUDA Unified Memory (`cudaMallocManaged`) under the hood, or use the VMM API to create mappings that fault on access. When the application touches a cold page, UVM faults to the driver, which pages it in from host.

3. **Proactive eviction**: Monitor memory pressure. When GPU memory is near capacity, evict least-recently-used pages to host. Use eBPF probes on UVM fault handlers to track access patterns.

4. **Transparent to application**: The application calls `cudaMalloc` and gets a pointer. It never knows some of its memory is on host. Performance degrades gracefully (PCIe bandwidth) rather than crashing with OOM.

**Real-world value:**
- Run Llama-3.1-70B (needs ~140GB for inference) on 2xA100-40GB instead of 2xA100-80GB
- Run fine-tuning that normally needs 8xH100 on 4xH100 with host paging for optimizer states
- Bin-pack multiple small models onto one GPU (3x 7B models on one 80GB GPU)

**Prior art:**
- **AntMan** (Alibaba, OSDI 2020): GPU memory oversubscription for DL training. Research prototype, not open-sourced for production use.
- **NVIDIA Unified Memory**: Provides the kernel-level page fault mechanism we'd build on top of. But UVM alone doesn't give you policy control — the hypervisor adds the intelligence.

**Effort**: ~8-12 weeks. The memory manager is the core complexity — eviction policy, fault handling, minimizing PCIe traffic. The interception layer is straightforward (reuse from checkpoint/restore).

### 4. GPU Time-Slicing with Preemption

True GPU scheduling: pause a workload mid-execution, run another, resume the first.

```
Time →

GPU without hypervisor:
|  Job A (100% GPU) .................................|  Job B starts after A finishes  |

GPU with hypervisor:
|  Job A  |  Job B  |  Job A  |  Job B  |  Job A  |  Job B  |
   50ms      50ms      50ms      50ms      50ms      50ms
```

**How it works:**

1. **Kernel launch interception**: Every `cudaLaunchKernel` / `cuLaunchKernel` goes through the hypervisor. The hypervisor maintains a per-process scheduling quantum.

2. **Preemption via checkpoint**: When process A's quantum expires:
   - `cudaDeviceSynchronize()` to drain in-flight kernels
   - Save A's GPU state (memory contents, stream state)
   - Load B's GPU state
   - Resume B

3. **Context switching cost**: Saving/restoring GPU state takes ~100ms-1s depending on working set size. This is too slow for fine-grained time-slicing (need <1ms) but works for **coarse-grained scheduling** (seconds to minutes):
   - Priority preemption: Serving request comes in → pause training → serve → resume
   - Fair-share: Two inference models share one GPU, each gets 50% of time

4. **Cooperative scheduling** (faster): Instead of full checkpoint, ask the application to yield. The hypervisor sends a signal, the application's framework yields after the current batch/request. This is what MPS does, but the hypervisor makes it transparent.

**Why not NVIDIA MPS?**
- MPS gives concurrent execution but no preemption — if one process is hogging the GPU, others wait
- MPS requires all processes to share one CUDA context — limits isolation
- MPS has no memory isolation — one process can corrupt another's memory
- The hypervisor provides true isolation with preemption

**Effort**: ~12-16 weeks. Context switching is the hard part — getting it fast enough to be useful. Cooperative scheduling (signal-based) is much easier (~4 weeks) and covers the high-value use case (serving preempts training).

### 5. Per-Process GPU Observability

Deep GPU metrics without CUPTI, without modifying the application, without NVIDIA profiler tools.

**What the hypervisor can report (from userspace interception):**

| Metric | Source | Granularity |
|--------|--------|-------------|
| GPU memory allocated per process | `cudaMalloc` interception | Per-allocation |
| GPU memory actual usage (RSS) | UVM fault tracking (eBPF) | Per-page |
| Kernel launches per second | `cuLaunchKernel` interception | Per-kernel |
| Kernel execution time | Pre/post stream sync | Per-kernel |
| Memory copy bandwidth (D2H/H2D) | `cudaMemcpy` interception | Per-copy |
| NCCL collective ops | `ncclAllReduce` etc. interception | Per-op |
| NCCL bandwidth / latency | Timing around collectives | Per-collective |
| Stream utilization | Stream create/sync tracking | Per-stream |
| cuBLAS/cuDNN op types | Handle + launch interception | Per-op |

**What eBPF adds (from ioctl observation):**

| Metric | Source | Granularity |
|--------|--------|-------------|
| GPU page faults | UVM ioctl tracing | Per-fault |
| GPU context switches | nvidia_ioctl tracing | Per-switch |
| Driver-level memory maps | ioctl decode | Per-mapping |
| MMIO register accesses | ioctl observation | Per-access |

**Output**: Prometheus metrics endpoint, just like NvSnap agent already has. Drop-in for Grafana dashboards.

**Why this matters**: Today, GPU observability requires either:
- `nvidia-smi` (per-device only, no per-process kernel-level stats)
- CUPTI (requires instrumenting the application, significant overhead)
- Nsight Systems (profiling tool, not production monitoring)

The hypervisor gives production-grade, per-process, zero-instrumentation GPU observability.

**Effort**: ~4-6 weeks. Most of the interception is shared with checkpoint/restore. The eBPF probes are the new piece (~2 weeks). Prometheus export reuses existing NvSnap metrics infrastructure.

### 6. GPU Fault Injection / Chaos Engineering

Test GPU failure handling in training and serving frameworks.

```
Hypervisor fault injection modes:
  - Drop N% of kernel launches (return cudaSuccess but don't launch)
  - Corrupt random memory regions (bit flips in GPU tensors)
  - Simulate ECC errors (return cudaErrorECCUncorrectable on random memcpy)
  - Inject latency into memory copies (sleep N ms in cudaMemcpy hook)
  - Kill NCCL communicators mid-collective (test fault tolerance)
  - Return OOM on cudaMalloc after N allocations (test memory pressure)
```

**Why this matters**: Nobody tests GPU failures systematically. When an H100 has an ECC error mid-training, frameworks typically crash unrecoverably. With fault injection, you can:
- Verify that training frameworks checkpoint correctly before crashes
- Test that serving frameworks failover cleanly
- Validate that NCCL timeout handling works in multi-node training
- Build confidence that your infra handles real GPU failures

**Effort**: ~2-3 weeks. The interception layer is already there. Fault injection is just conditional logic in the hooks.

## Implementation Strategy

### Phase 0: NvSnap Multi-GPU (in progress, #25)

What we're building now:
- `cudaMalloc` / `cudaFree` interception and tracking
- GPU memory save (D2H) and restore (H2D) with VA preservation
- NCCL communicator destroy/reconstruct
- Stream/event tracking (if needed)

This is the foundation. Everything else builds on it.

### Phase 1: Observability + Fault Injection (4-8 weeks after Phase 0)

Low-hanging fruit, high value, validates the architecture:

1. **GPU metrics from userspace hooks** — per-process memory, kernel launches, NCCL ops
2. **eBPF ioctl tracing** — driver-level observability without modifying anything
3. **Prometheus export** — reuse NvSnap's existing metrics infrastructure
4. **Basic fault injection** — OOM simulation, kernel drop, latency injection

Deliverable: `libnvsnap_gpu.so` that any CUDA application can load for deep observability. Standalone value even without checkpoint/restore.

### Phase 2: Memory Oversubscription (8-12 weeks)

Build on Phase 0's memory tracking:

1. **Host memory pool** — pre-allocated host buffer for GPU page eviction
2. **Eviction policy** — LRU based on access tracking (UVM faults or explicit touch tracking)
3. **Transparent paging** — `cudaMalloc` returns VMM-backed pointers, faults handled transparently
4. **Memory pressure API** — let the scheduler query "how much could this pod give up?"

Deliverable: Run GPU workloads that exceed physical GPU memory. Kubernetes scheduler can overcommit GPU memory.

### Phase 3: Live Migration (8-12 weeks, can parallel with Phase 2)

Build on Phase 0's checkpoint/restore:

1. **Device ordinal remapping** — intercept `cudaSetDevice`, `cuCtxCreate`, remap device IDs
2. **Streaming checkpoint transfer** — overlap GPU memory transfer with process image transfer
3. **Pre-copy migration** — iteratively transfer memory pages while workload runs, then final freeze + transfer of dirty pages
4. **Kubernetes integration** — CRD for migration requests, controller that coordinates source/dest agents

Deliverable: `kubectl nvsnap migrate pod/vllm-serving --to-node gpu-node-2`

### Phase 4: GPU Scheduling (12-16 weeks)

The full hypervisor:

1. **Cooperative scheduling** — signal-based yield between processes
2. **Priority preemption** — checkpoint low-priority, run high-priority, restore
3. **Fair-share policy** — configurable per-namespace GPU time quotas
4. **Kubernetes scheduler plugin** — GPU-aware scheduling with oversubscription and preemption

Deliverable: Multiple pods share a single GPU with real isolation and preemption.

## Competitive Landscape

| System | Type | Memory Oversub | Live Migration | Preemption | Observability | Open Source |
|--------|------|---------------|----------------|------------|---------------|-------------|
| NVIDIA MIG | Hardware partition | No | No | No | nvidia-smi | N/A |
| NVIDIA MPS | Cooperative sharing | No | No | No | Basic | N/A |
| NVIDIA vGPU | Hypervisor (bare metal) | Limited | No | No | Basic | No |
| Run:ai | Scheduler | No | No | Priority | Dashboard | No |
| Cedana | Checkpoint/restore | No | Planned | No | No | No |
| gVirtuS | API forwarding | No | Partial | No | No | Yes (stale) |
| rCUDA | Remote GPU | No | Partial | No | No | Yes (academic) |
| AntMan | Memory management | Yes (research) | No | No | No | No |
| Gandiva | Scheduling | No | Partial | Yes (research) | No | No |
| **NvSnap GPU Hypervisor** | **Full stack** | **Phase 2** | **Phase 3** | **Phase 4** | **Phase 1** | **Yes** |

The gap in the market: nobody ships a full-stack, open-source GPU hypervisor that does all four. Each existing system does one piece.

## Product Directions

### Option A: Extend NvSnap

Keep it as part of the NvSnap checkpoint/restore project. The hypervisor is "NvSnap Pro" or "NvSnap Enterprise." The open-source core is checkpoint/restore + observability. The commercial layer adds migration, oversubscription, scheduling.

**Pros**: Existing brand, existing users, natural extension.
**Cons**: Scope creep — NvSnap is about checkpoint/restore, a GPU hypervisor is about GPU infrastructure broadly.

### Option B: Spin Off as Separate Product

New project: **nvsnap-gpu** or a new brand entirely. The hypervisor is the product. Checkpoint/restore is one feature of the hypervisor, not the other way around.

**Pros**: Clean positioning ("GPU hypervisor" is a bigger market than "GPU checkpoint tool"). Separate community, separate roadmap.
**Cons**: Splits effort, need to maintain two projects, shared code needs clean interfaces.

### Option C: Library + Platform

Ship `libnvsnap_gpu.so` as an open-source library (the interception + observability layer). Build a commercial platform on top (scheduling, migration, multi-cluster GPU management).

**Pros**: Maximum adoption of the library (everyone can use it). Commercial value in the platform layer. Similar to how Envoy (library) + Istio (platform) work.
**Cons**: Need to find the right boundary between library and platform.

## Open Questions

1. **ioctl compatibility**: How much effort per NVIDIA driver version to decode ioctls? Is it 2 days or 2 weeks per version? Need to reverse-engineer one version to calibrate.

2. **UVM performance**: Can we use UVM-based paging for oversubscription without killing inference latency? UVM page faults add ~10us per fault. For serving workloads, that might be too much.

3. **Context switch speed**: How fast can we save/restore GPU state for time-slicing? If it's >1s, preemption is only useful for coarse-grained scheduling (training vs serving priority). Fine-grained (<10ms) requires hardware support (NVIDIA doesn't expose this).

4. **Kernel-level vs userspace-only**: Can we build the full vision with userspace-only interposition? Or do some capabilities (memory oversubscription, preemption) fundamentally require kernel module support? Need to prototype to know.

5. **Market timing**: NVIDIA is slowly adding some of these capabilities (MIG improvements, better MPS, CUDA Graphs preemption). Do we build ahead of them or focus on what they'll never open-source?

## References

- `$HOME/CUDACallInterception.pdf` — Survey of CUDA interception strategies
- `docs/CUDA-INTERPOSITION-DESIGN.md` — NvSnap's multi-GPU interposition design
- `tests/cuda_interpose/` — Feasibility tests for CUDA VA preservation
- AntMan: Dynamic Scaling on GPU Clusters for Deep Learning (OSDI 2020)
- Gandiva: Introspective Cluster Scheduling for Deep Learning (OSDI 2018)
- Salus: Fine-Grained GPU Sharing Primitives for Deep Learning Applications (MLSys 2020)
- gVirtuS: GPU Virtualization Framework (github.com/gvirtus)
- CRIUgpu: GPU Process Checkpoint/Restore (arxiv.org/abs/2502.16631)

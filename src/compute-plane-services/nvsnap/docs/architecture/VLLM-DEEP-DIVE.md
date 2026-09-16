# vLLM Checkpoint/Restore: Deep Technical Analysis

## Executive Summary

This document provides an in-depth analysis of checkpointing vLLM workloads. vLLM represents the most complex workload category we aim to support, featuring:

- Multiple cooperating processes
- GPU-to-GPU communication via NCCL
- CUDA Inter-Process Communication (IPC)
- Complex memory management (PagedAttention)
- Continuous batching with in-flight requests
- Optional Ray integration

**Our constraint: Zero application modification.**

---

## Table of Contents

1. [vLLM Architecture Analysis](#vllm-architecture-analysis)
2. [State Decomposition](#state-decomposition)
3. [The NCCL Problem](#the-nccl-problem)
4. [Checkpoint Strategy](#checkpoint-strategy)
5. [Restore Strategy](#restore-strategy)
6. [Alternative Approaches](#alternative-approaches)
7. [Implementation Roadmap](#implementation-roadmap)
8. [Risk Analysis](#risk-analysis)

---

## vLLM Architecture Analysis

### Deployment Modes

vLLM can run in several configurations:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        vLLM DEPLOYMENT MODES                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  MODE 1: Single GPU (Simplest)                                              │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                                                                      │   │
│  │  ┌──────────────────────────────────────────────────────────────┐   │   │
│  │  │                      Single Process                          │   │   │
│  │  │                                                               │   │   │
│  │  │  API Server → Engine → Model (GPU 0)                         │   │   │
│  │  │                                                               │   │   │
│  │  └──────────────────────────────────────────────────────────────┘   │   │
│  │                                                                      │   │
│  │  Complexity: LOW                                                     │   │
│  │  - Single process, single GPU                                        │   │
│  │  - No IPC, no NCCL                                                  │   │
│  │  - Standard checkpoint approach works                               │   │
│  │                                                                      │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  MODE 2: Tensor Parallel (Common for large models)                          │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                                                                      │   │
│  │  ┌─────────────┐                                                    │   │
│  │  │ API Server  │ (may be in engine process)                         │   │
│  │  └──────┬──────┘                                                    │   │
│  │         │                                                            │   │
│  │  ┌──────▼──────┐                                                    │   │
│  │  │   Engine    │ Scheduler, KV Cache Manager                        │   │
│  │  └──────┬──────┘                                                    │   │
│  │         │ multiprocessing / Ray                                      │   │
│  │   ┌─────┼─────┬─────────┬─────────┐                                 │   │
│  │   │     │     │         │         │                                 │   │
│  │   ▼     ▼     ▼         ▼         ▼                                 │   │
│  │  ┌───┐ ┌───┐ ┌───┐    ┌───┐    ┌───┐                               │   │
│  │  │W0 │ │W1 │ │W2 │    │W3 │    │WN │  Workers                      │   │
│  │  │   │ │   │ │   │    │   │    │   │  (one per GPU)               │   │
│  │  └─┬─┘ └─┬─┘ └─┬─┘    └─┬─┘    └─┬─┘                               │   │
│  │    │     │     │        │        │                                  │   │
│  │    │     └──NCCL Ring───┴────────┘                                  │   │
│  │    │              All-Reduce                                        │   │
│  │  [GPU0] [GPU1] [GPU2] [GPU3]  [GPUN]                               │   │
│  │                                                                      │   │
│  │  Complexity: HIGH                                                    │   │
│  │  - Multiple processes with NCCL                                     │   │
│  │  - Shared memory for coordination                                   │   │
│  │  - CUDA IPC for tensor sharing                                      │   │
│  │                                                                      │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  MODE 3: Pipeline Parallel + Tensor Parallel (Multi-node)                   │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                                                                      │   │
│  │  Node 1                           Node 2                            │   │
│  │  ┌──────────────────────────┐    ┌──────────────────────────┐      │   │
│  │  │ Layers 0-15              │    │ Layers 16-31             │      │   │
│  │  │ ┌────┐ ┌────┐           │    │ ┌────┐ ┌────┐            │      │   │
│  │  │ │W0  │ │W1  │  TP=2     │◄──►│ │W2  │ │W3  │  TP=2     │      │   │
│  │  │ └────┘ └────┘           │ N  │ └────┘ └────┘            │      │   │
│  │  │   │      │              │ C  │   │      │               │      │   │
│  │  │   │NCCL  │              │ C  │   │NCCL  │               │      │   │
│  │  │   └──────┘              │ L  │   └──────┘               │      │   │
│  │  └──────────────────────────┘    └──────────────────────────┘      │   │
│  │                                                                      │   │
│  │  Complexity: EXTREME                                                 │   │
│  │  - Cross-node NCCL                                                  │   │
│  │  - TCP/RDMA connections                                             │   │
│  │  - Must checkpoint all nodes atomically                             │   │
│  │                                                                      │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Internal Components

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        vLLM INTERNAL COMPONENTS                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                           LLMEngine                                   │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                        Scheduler                                 │ │   │
│  │  │  • Continuous batching                                          │ │   │
│  │  │  • Request queuing                                              │ │   │
│  │  │  • Preemption policy                                            │ │   │
│  │  │  • Block manager                                                │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                     KV Cache Manager                            │ │   │
│  │  │  • PagedAttention blocks                                        │ │   │
│  │  │  • Block allocation table                                       │ │   │
│  │  │  • Copy-on-write for beam search                               │ │   │
│  │  │  • Prefix caching                                               │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                     Model Runner                                 │ │   │
│  │  │  • Execute model forward pass                                   │ │   │
│  │  │  • Manage CUDA graphs (if enabled)                             │ │   │
│  │  │  • Handle sampling                                              │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                           Worker Process                              │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                     Model Weights                                │ │   │
│  │  │  • Sharded across GPUs (TP)                                     │ │   │
│  │  │  • Loaded from HuggingFace / local                             │ │   │
│  │  │  • Quantized (AWQ, GPTQ) or FP16/BF16                         │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                     KV Cache Blocks                              │ │   │
│  │  │  • Pre-allocated GPU memory                                     │ │   │
│  │  │  • Managed by PagedAttention                                    │ │   │
│  │  │  • Can be 10s of GBs                                           │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │                     NCCL Communicator                           │ │   │
│  │  │  • For tensor parallel all-reduce                              │ │   │
│  │  │  • Topology-aware (NVLink, PCIe)                              │ │   │
│  │  │  • CUDA streams                                                 │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                       │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## State Decomposition

### What State Must Be Captured?

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        STATE INVENTORY                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  CATEGORY 1: CPU STATE (per process)                                        │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  ✓ Process memory (heap, stack, mmap)                                  │ │
│  │  ✓ Open file descriptors                                               │ │
│  │  ✓ Thread state (registers, TLS)                                       │ │
│  │  ✓ Signal handlers                                                      │ │
│  │  ✓ Environment variables                                                │ │
│  │  ✓ Working directory                                                    │ │
│  │                                                                         │ │
│  │  Method: CRIU                                                           │ │
│  │  Difficulty: SOLVED (CRIU handles this well)                           │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CATEGORY 2: GPU MEMORY (per GPU per process)                               │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  ✓ Model weights (static after load)                     ~14GB (7B)   │ │
│  │  ✓ KV cache blocks (dynamic)                             ~30GB+       │ │
│  │  ✓ Activation memory (temporary)                         ~2GB         │ │
│  │  ✓ CUDA workspace (cuBLAS, cuDNN)                        ~1GB         │ │
│  │                                                                         │ │
│  │  Method: cuMemcpy D2H + allocation tracking                            │ │
│  │  Difficulty: MEDIUM (large but straightforward)                        │ │
│  │                                                                         │ │
│  │  OPTIMIZATION: Model weights can be reloaded on restore               │ │
│  │  - Checkpoint only KV cache + state                                    │ │
│  │  - Reduces checkpoint size by ~14GB per GPU                           │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CATEGORY 3: GPU CONTEXT STATE                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  ! CUDA contexts (opaque driver state)                                 │ │
│  │  ! CUDA streams and their ordering                                     │ │
│  │  ! CUDA events and dependencies                                        │ │
│  │  ! CUDA graphs (if used)                                               │ │
│  │  ! cuDNN/cuBLAS handles                                                │ │
│  │                                                                         │ │
│  │  Method: Quiesce + reconstruct on restore                              │ │
│  │  Difficulty: HIGH (need to intercept CUDA init)                        │ │
│  │                                                                         │ │
│  │  KEY INSIGHT: If we quiesce properly, most of this state              │ │
│  │  is reconstructable by letting the app reinitialize.                  │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CATEGORY 4: INTER-PROCESS STATE                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  ! Shared memory segments (/dev/shm/*)                                 │ │
│  │  ! POSIX semaphores                                                     │ │
│  │  ! Unix domain sockets                                                  │ │
│  │  ! CUDA IPC memory handles                                              │ │
│  │  ! CUDA IPC event handles                                               │ │
│  │                                                                         │ │
│  │  Method: Enumerate + dump + reconstruct                                │ │
│  │  Difficulty: HIGH (need careful mapping)                               │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CATEGORY 5: NCCL STATE                                                      │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  !! NCCL communicators (opaque)                                        │ │
│  │  !! Ring/tree topology                                                  │ │
│  │  !! In-flight collectives                                              │ │
│  │  !! Internal buffers                                                   │ │
│  │  !! GPU-GPU connections                                                 │ │
│  │                                                                         │ │
│  │  Method: Cannot checkpoint directly!                                   │ │
│  │  Solution: Quiesce all ops → let NCCL reinit on restore               │ │
│  │  Difficulty: VERY HIGH                                                  │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CATEGORY 6: APPLICATION STATE                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  • In-flight requests (request ID, tokens generated, KV blocks)       │ │
│  │  • Scheduler state (waiting queue, running batch)                     │ │
│  │  • Block allocation table                                              │ │
│  │  • Sequence metadata                                                   │ │
│  │                                                                         │ │
│  │  Method: Captured in CPU memory                                        │ │
│  │  Note: May lose partial responses if not drained                      │ │
│  │  Difficulty: LOW (handled by CPU checkpoint)                           │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## The NCCL Problem

### Why NCCL Is The Hardest Part

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        THE NCCL PROBLEM                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  NCCL (NVIDIA Collective Communications Library) is a black box:            │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                                                                         │ │
│  │  Application                                                            │ │
│  │      │                                                                  │ │
│  │      ▼                                                                  │ │
│  │  ncclAllReduce(sendbuff, recvbuff, count, datatype, op, comm, stream)  │ │
│  │      │                                                                  │ │
│  │      ▼                                                                  │ │
│  │  ┌───────────────────────────────────────────────────────────────────┐ │ │
│  │  │                    NCCL BLACK BOX                                  │ │ │
│  │  │                                                                    │ │ │
│  │  │  • Opaque ncclComm_t handle                                       │ │ │
│  │  │  • Internal topology detection                                     │ │ │
│  │  │  • Ring/tree algorithm selection                                  │ │ │
│  │  │  • GPU-GPU connections (P2P, SHM, NET)                           │ │ │
│  │  │  • Internal memory pools                                          │ │ │
│  │  │  • Async execution on CUDA streams                                │ │ │
│  │  │                                                                    │ │ │
│  │  │  WE CANNOT SEE OR SAVE THIS STATE!                                │ │ │
│  │  │                                                                    │ │ │
│  │  └───────────────────────────────────────────────────────────────────┘ │ │
│  │      │                                                                  │ │
│  │      ▼                                                                  │ │
│  │  GPU 0 ◄────────────► GPU 1 ◄────────────► GPU 2                      │ │
│  │           NVLink              PCIe                                      │ │
│  │                                                                         │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│                                                                              │
│  THE CHALLENGES:                                                             │
│                                                                              │
│  1. OPAQUE STATE                                                            │
│     • ncclComm_t is a pointer to internal structures                       │
│     • No API to serialize/deserialize                                       │
│     • Version-specific internal layouts                                     │
│                                                                              │
│  2. IN-FLIGHT OPERATIONS                                                     │
│     • Collectives are async on CUDA streams                                │
│     • Cannot interrupt mid-collective                                       │
│     • Must wait for completion                                              │
│                                                                              │
│  3. TOPOLOGY BINDING                                                         │
│     • NCCL detects NVLink, PCIe topology at init                          │
│     • Optimizes algorithms for specific hardware                           │
│     • New node may have different topology                                 │
│                                                                              │
│  4. DISTRIBUTED STATE                                                        │
│     • Each rank has partial state                                           │
│     • Must coordinate across all ranks                                      │
│     • Network connections between nodes                                     │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Our NCCL Strategy

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        NCCL CHECKPOINT STRATEGY                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  APPROACH: DON'T CHECKPOINT NCCL STATE - REINITIALIZE ON RESTORE            │
│                                                                              │
│  This works because:                                                         │
│  1. NCCL is stateless between collectives                                   │
│  2. Communicators can be recreated with same configuration                  │
│  3. Application state (which collective to run) is in CPU memory            │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                                                                         │ │
│  │  STEP 1: QUIESCE NCCL                                                  │ │
│  │  ┌───────────────────────────────────────────────────────────────────┐ │ │
│  │  │                                                                    │ │ │
│  │  │  For each NCCL stream:                                            │ │ │
│  │  │    cudaStreamSynchronize(nccl_stream)                             │ │ │
│  │  │                                                                    │ │ │
│  │  │  This ensures all collectives have completed.                     │ │ │
│  │  │                                                                    │ │ │
│  │  │  Challenge: Finding NCCL streams                                  │ │ │
│  │  │  Solution: Intercept ncclAllReduce etc. to track streams         │ │ │
│  │  │            OR synchronize ALL streams (cudaDeviceSynchronize)    │ │ │
│  │  │                                                                    │ │ │
│  │  └───────────────────────────────────────────────────────────────────┘ │ │
│  │                                                                         │ │
│  │  STEP 2: SAVE CONFIGURATION                                            │ │
│  │  ┌───────────────────────────────────────────────────────────────────┐ │ │
│  │  │                                                                    │ │ │
│  │  │  For each ncclComm_t:                                             │ │ │
│  │  │    • Rank in this communicator                                    │ │ │
│  │  │    • World size                                                   │ │ │
│  │  │    • Unique ID (ncclUniqueId)                                    │ │ │
│  │  │                                                                    │ │ │
│  │  │  How to get this:                                                 │ │ │
│  │  │    • Intercept ncclCommInitRank() via LD_PRELOAD                 │ │ │
│  │  │    • Record parameters when communicator is created              │ │ │
│  │  │                                                                    │ │ │
│  │  └───────────────────────────────────────────────────────────────────┘ │ │
│  │                                                                         │ │
│  │  STEP 3: DESTROY COMMUNICATORS (optional)                              │ │
│  │  ┌───────────────────────────────────────────────────────────────────┐ │ │
│  │  │                                                                    │ │ │
│  │  │  ncclCommDestroy(comm) for each communicator                      │ │ │
│  │  │                                                                    │ │ │
│  │  │  This releases GPU-GPU connections and resources.                 │ │ │
│  │  │  Makes checkpoint cleaner.                                        │ │ │
│  │  │                                                                    │ │ │
│  │  │  Alternative: Leave them, they'll be invalid after restore       │ │ │
│  │  │                                                                    │ │ │
│  │  └───────────────────────────────────────────────────────────────────┘ │ │
│  │                                                                         │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  ON RESTORE:                                                                 │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                                                                         │ │
│  │  STEP 1: RESTORE CPU STATE (CRIU)                                      │ │
│  │  • Processes restored, paused before first CUDA call                   │ │
│  │                                                                         │ │
│  │  STEP 2: INTERCEPT FIRST NCCL CALL                                     │ │
│  │  • Our LD_PRELOAD intercepts ncclAllReduce (or similar)               │ │
│  │  • Detects this is first call after restore                           │ │
│  │                                                                         │ │
│  │  STEP 3: REINITIALIZE COMMUNICATORS                                     │ │
│  │  • Generate new ncclUniqueId (all ranks must coordinate!)             │ │
│  │  • ncclCommInitRank() with saved rank/world_size                      │ │
│  │  • Replace old (invalid) ncclComm_t handles                           │ │
│  │                                                                         │ │
│  │  STEP 4: CONTINUE EXECUTION                                             │ │
│  │  • Application continues unaware of reinitialization                   │ │
│  │  • Next collective uses new communicator                               │ │
│  │                                                                         │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  CRITICAL REQUIREMENT:                                                       │
│  • All ranks must checkpoint at the same logical point                      │
│  • All ranks must restore together                                          │
│  • New ncclUniqueId must be distributed to all ranks                       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### NCCL Interception Library

> **Status: design, not shipped.** Process-level multi-GPU checkpoint/restore
> (which is what the NCCL interception below would support) is blocked upstream
> on a `libcudart` per-process GPU-state replay limitation. The shipping
> multi-GPU path is rootfs cache-distribution (see the README and
> `docs/MULTI-GPU-ROOTFS-FANOUT-DESIGN.md`). The code below is a design sketch,
> not a description of `lib/nvsnap_intercept/`.

```c
// Design sketch (not shipped) — NCCL interception for multi-GPU process C/R

#include <nccl.h>
#include <dlfcn.h>
#include <pthread.h>
#include <stdbool.h>

// Track all communicators
typedef struct {
    ncclComm_t comm;
    int rank;
    int nranks;
    ncclUniqueId unique_id;
    bool valid;
} CommInfo;

static CommInfo comm_registry[MAX_COMMS];
static int comm_count = 0;
static pthread_mutex_t registry_lock = PTHREAD_MUTEX_INITIALIZER;
static bool restore_mode = false;

// Real NCCL function pointers
static ncclResult_t (*real_ncclCommInitRank)(ncclComm_t*, int, ncclUniqueId, int) = NULL;
static ncclResult_t (*real_ncclAllReduce)(const void*, void*, size_t, ncclDataType_t, 
                                           ncclRedOp_t, ncclComm_t, cudaStream_t) = NULL;
static ncclResult_t (*real_ncclCommDestroy)(ncclComm_t) = NULL;

__attribute__((constructor))
static void init_nccl_intercept() {
    real_ncclCommInitRank = dlsym(RTLD_NEXT, "ncclCommInitRank");
    real_ncclAllReduce = dlsym(RTLD_NEXT, "ncclAllReduce");
    real_ncclCommDestroy = dlsym(RTLD_NEXT, "ncclCommDestroy");
    
    // Check if we're in restore mode
    restore_mode = (getenv("NVSNAP_RESTORE_MODE") != NULL);
}

// Intercept ncclCommInitRank
ncclResult_t ncclCommInitRank(ncclComm_t* comm, int nranks, ncclUniqueId commId, int rank) {
    ncclResult_t result = real_ncclCommInitRank(comm, nranks, commId, rank);
    
    if (result == ncclSuccess) {
        pthread_mutex_lock(&registry_lock);
        
        // Record this communicator
        comm_registry[comm_count].comm = *comm;
        comm_registry[comm_count].rank = rank;
        comm_registry[comm_count].nranks = nranks;
        comm_registry[comm_count].unique_id = commId;
        comm_registry[comm_count].valid = true;
        comm_count++;
        
        pthread_mutex_unlock(&registry_lock);
    }
    
    return result;
}

// Intercept ncclAllReduce (and other collectives)
ncclResult_t ncclAllReduce(const void* sendbuff, void* recvbuff, size_t count,
                           ncclDataType_t datatype, ncclRedOp_t op,
                           ncclComm_t comm, cudaStream_t stream) {
    
    // In restore mode, reinitialize communicators on first collective
    if (restore_mode && needs_reinit(comm)) {
        reinitialize_communicators();
        restore_mode = false;  // Only do this once
    }
    
    // Get the correct (possibly new) communicator
    ncclComm_t actual_comm = get_actual_comm(comm);
    
    return real_ncclAllReduce(sendbuff, recvbuff, count, datatype, op, actual_comm, stream);
}

// Called by NVSNAP agent to get communicator info for checkpoint
void nvsnap_get_nccl_state(CommInfo** info, int* count) {
    pthread_mutex_lock(&registry_lock);
    *info = comm_registry;
    *count = comm_count;
    pthread_mutex_unlock(&registry_lock);
}

// Called during restore to reinitialize all communicators
static void reinitialize_communicators() {
    // 1. Coordinate with other ranks to get new unique ID
    //    (via shared memory or coordinator service)
    ncclUniqueId new_id = coordinate_new_unique_id();
    
    // 2. Destroy old (invalid) communicators
    for (int i = 0; i < comm_count; i++) {
        // Old comm is invalid, but we track the handle
        comm_registry[i].valid = false;
    }
    
    // 3. Create new communicators with same rank/world_size
    for (int i = 0; i < comm_count; i++) {
        ncclComm_t new_comm;
        ncclResult_t res = real_ncclCommInitRank(
            &new_comm,
            comm_registry[i].nranks,
            new_id,
            comm_registry[i].rank
        );
        
        if (res == ncclSuccess) {
            // Update registry with new communicator
            comm_registry[i].comm = new_comm;
            comm_registry[i].unique_id = new_id;
            comm_registry[i].valid = true;
        }
    }
}
```

---

## Checkpoint Strategy

### Complete Checkpoint Flow for vLLM

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        vLLM CHECKPOINT FLOW                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  PHASE 1: PREPARATION (can happen async, before freeze)                     │
│  ═══════════════════════════════════════════════════════                    │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  1.1 REQUEST DRAIN                                                   │   │
│  │      • Signal vLLM proxy to stop accepting new requests             │   │
│  │      • Wait for in-flight requests to complete (with timeout)       │   │
│  │      • Alternative: Force checkpoint, lose partial responses        │   │
│  │                                                                      │   │
│  │  1.2 GPU MEMORY PRE-COPY (optional optimization)                    │   │
│  │      • Start copying GPU memory to host in background               │   │
│  │      • Reduces freeze time                                          │   │
│  │      • Need to track dirty pages for consistency                    │   │
│  │                                                                      │   │
│  │  1.3 PREPARE STORAGE                                                │   │
│  │      • Create checkpoint directory                                  │   │
│  │      • Ensure sufficient space                                      │   │
│  │      • Set up streaming upload if using cloud storage               │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 2: FREEZE                                                             │
│  ═══════════════════                                                         │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  2.1 CGROUP FREEZE                                                   │   │
│  │      echo FROZEN > /sys/fs/cgroup/.../cgroup.freeze                 │   │
│  │      • All processes stop atomically                                │   │
│  │      • Guaranteed consistent state                                  │   │
│  │                                                                      │   │
│  │  2.2 VERIFY FREEZE                                                   │   │
│  │      • Check all processes in S (sleeping) or D (disk) state       │   │
│  │      • No runnable processes                                        │   │
│  │                                                                      │   │
│  │  2.3 GPU SYNCHRONIZATION                                            │   │
│  │      • For each process with GPU context:                          │   │
│  │        - Inject cudaDeviceSynchronize() via ptrace                 │   │
│  │        - Wait for all GPU ops to complete                          │   │
│  │      • This ensures GPU state is consistent                        │   │
│  │                                                                      │   │
│  │  FREEZE TIME TARGET: < 100ms                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 3: CAPTURE                                                            │
│  ════════════════════                                                        │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                                                                      │   │
│  │  PARALLEL CAPTURE (all happen simultaneously):                      │   │
│  │                                                                      │   │
│  │  ┌─────────────────────┐  ┌─────────────────────┐                   │   │
│  │  │  3.1 CRIU CAPTURE   │  │  3.2 GPU CAPTURE    │                   │   │
│  │  │                     │  │                     │                   │   │
│  │  │  For each process:  │  │  For each GPU:      │                   │   │
│  │  │  • Memory dump      │  │  • Dump allocations │                   │   │
│  │  │  • FD table         │  │  • Stream to disk   │                   │   │
│  │  │  • Register state   │  │  • Record metadata  │                   │   │
│  │  │  • Signal handlers  │  │                     │                   │   │
│  │  │  • Thread state     │  │  Size: 10s of GBs   │                   │   │
│  │  │                     │  │  per GPU            │                   │   │
│  │  └─────────────────────┘  └─────────────────────┘                   │   │
│  │                                                                      │   │
│  │  ┌─────────────────────┐  ┌─────────────────────┐                   │   │
│  │  │  3.3 IPC CAPTURE    │  │  3.4 NCCL CAPTURE   │                   │   │
│  │  │                     │  │                     │                   │   │
│  │  │  • Shared memory    │  │  • Comm registry    │                   │   │
│  │  │  • Semaphores       │  │  • Rank info        │                   │   │
│  │  │  • Unix sockets     │  │  • World size       │                   │   │
│  │  │  • CUDA IPC handles │  │                     │                   │   │
│  │  │                     │  │                     │                   │   │
│  │  └─────────────────────┘  └─────────────────────┘                   │   │
│  │                                                                      │   │
│  │  CAPTURE TIME TARGET: < 60s for 100GB total state                  │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 4: PACKAGE                                                            │
│  ════════════════════                                                        │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  4.1 ASSEMBLE BUNDLE                                                 │   │
│  │                                                                      │   │
│  │  checkpoint-vllm-xxxxxx/                                            │   │
│  │  ├── manifest.json           # Metadata, versions, checksums       │   │
│  │  ├── topology.json           # Process tree, GPU mapping           │   │
│  │  ├── processes/                                                     │   │
│  │  │   ├── 1234/               # Main engine process                 │   │
│  │  │   │   ├── criu/           # CRIU images                         │   │
│  │  │   │   ├── gpu-0.bin       # GPU 0 memory (zstd compressed)     │   │
│  │  │   │   ├── gpu-allocs.json # Allocation map                      │   │
│  │  │   │   └── metadata.json   # Process metadata                    │   │
│  │  │   ├── 1235/               # Worker 0                            │   │
│  │  │   │   ├── criu/                                                 │   │
│  │  │   │   ├── gpu-0.bin                                             │   │
│  │  │   │   └── ...                                                   │   │
│  │  │   └── 1236/               # Worker 1                            │   │
│  │  │       └── ...                                                   │   │
│  │  ├── shared/                                                        │   │
│  │  │   ├── shm-nccl-xxxxx.bin  # Shared memory segments             │   │
│  │  │   └── shm-manifest.json   # SHM key mapping                     │   │
│  │  ├── ipc/                                                           │   │
│  │  │   ├── cuda-ipc.json       # CUDA IPC handle mappings           │   │
│  │  │   └── socket-pairs.json   # Unix socket relationships          │   │
│  │  ├── nccl/                                                          │   │
│  │  │   └── comms.json          # Communicator configuration          │   │
│  │  └── checksums.sha256                                               │   │
│  │                                                                      │   │
│  │  4.2 COMPRESS (streaming)                                           │   │
│  │      • zstd level 3 (good balance)                                  │   │
│  │      • Parallel compression                                         │   │
│  │                                                                      │   │
│  │  4.3 UPLOAD                                                          │   │
│  │      • Stream to S3/GCS                                             │   │
│  │      • Multi-part upload                                            │   │
│  │      • Retry on failure                                             │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 5: CLEANUP                                                            │
│  ════════════════════                                                        │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  Option A: TERMINATE                                                 │   │
│  │      • Kill all processes                                           │   │
│  │      • Release GPU resources                                        │   │
│  │      • Standard for spot preemption                                 │   │
│  │                                                                      │   │
│  │  Option B: CONTINUE (leave-running)                                  │   │
│  │      • Unfreeze cgroup                                              │   │
│  │      • Processes resume                                             │   │
│  │      • For scheduled backups                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Restore Strategy

### Complete Restore Flow for vLLM

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        vLLM RESTORE FLOW                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  PHASE 1: PREPARE ENVIRONMENT                                                │
│  ════════════════════════════════                                            │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  1.1 DOWNLOAD CHECKPOINT                                             │   │
│  │      • Stream from S3/GCS                                            │   │
│  │      • Verify checksums                                              │   │
│  │      • Decompress                                                    │   │
│  │                                                                      │   │
│  │  1.2 VALIDATE ENVIRONMENT                                            │   │
│  │      • Check GPU availability                                        │   │
│  │      • Check driver/CUDA version compatibility                       │   │
│  │      • Ensure sufficient GPU memory                                  │   │
│  │                                                                      │   │
│  │  1.3 PREPARE CONTAINER                                               │   │
│  │      • Create container with matching spec                          │   │
│  │      • Mount GPU devices                                             │   │
│  │      • Set up network namespace                                      │   │
│  │      • Inject our LD_PRELOAD libraries                              │   │
│  │                                                                      │   │
│  │  1.4 PREPARE IPC                                                     │   │
│  │      • Recreate shared memory segments (same keys)                  │   │
│  │      • Restore shm contents                                          │   │
│  │      • Set up coordination mechanism for NCCL reinit                │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 2: CRIU RESTORE                                                       │
│  ══════════════════════                                                      │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  2.1 RESTORE PROCESS TREE                                            │   │
│  │      • CRIU restores in order: parent → children                   │   │
│  │      • Processes created but stopped immediately                    │   │
│  │      • PID remapping if needed                                       │   │
│  │                                                                      │   │
│  │  2.2 FD REMAPPING                                                    │   │
│  │      • GPU device FDs are invalid (old /dev/nvidia*)               │   │
│  │      • Our hook will handle GPU reinitialization                    │   │
│  │      • Network sockets may need special handling                    │   │
│  │                                                                      │   │
│  │  2.3 MEMORY RESTORATION                                              │   │
│  │      • CPU memory restored by CRIU                                  │   │
│  │      • GPU memory handled next                                       │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 3: GPU RESTORATION                                                    │
│  ═════════════════════════                                                   │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  For each process (via our LD_PRELOAD intercept):                   │   │
│  │                                                                      │   │
│  │  3.1 INTERCEPT CUDA INIT                                             │   │
│  │      • Process attempts cuInit() or similar                         │   │
│  │      • Our library intercepts                                        │   │
│  │      • Detects we're in restore mode                                │   │
│  │                                                                      │   │
│  │  3.2 INITIALIZE CUDA CONTEXT                                         │   │
│  │      • Let real cuInit() proceed                                    │   │
│  │      • New context on (possibly different) GPU                      │   │
│  │                                                                      │   │
│  │  3.3 RECREATE ALLOCATIONS                                            │   │
│  │      • Read allocation map from checkpoint                          │   │
│  │      • cudaMalloc for each allocation                               │   │
│  │                                                                      │   │
│  │      CHALLENGE: Same virtual addresses?                             │   │
│  │      ┌──────────────────────────────────────────────────────────┐   │   │
│  │      │  STRATEGY A: Try to get same addresses                   │   │   │
│  │      │  • cudaMalloc until we get right address                 │   │   │
│  │      │  • Works if GPU memory layout is similar                 │   │   │
│  │      │                                                           │   │   │
│  │      │  STRATEGY B: Pointer remapping                           │   │   │
│  │      │  • Record old→new address mapping                        │   │   │
│  │      │  • Scan CPU memory for GPU pointers                      │   │   │
│  │      │  • Update all pointers                                   │   │   │
│  │      │  • Complex but reliable                                  │   │   │
│  │      │                                                           │   │   │
│  │      │  STRATEGY C: Use reserved address space                  │   │   │
│  │      │  • Reserve GPU VA space at process start                 │   │   │
│  │      │  • Always allocate in reserved region                    │   │   │
│  │      │  • Addresses are reproducible                            │   │   │
│  │      └──────────────────────────────────────────────────────────┘   │   │
│  │                                                                      │   │
│  │  3.4 COPY DATA BACK TO GPU                                           │   │
│  │      • cudaMemcpy H2D for each allocation                           │   │
│  │      • Parallel transfers across GPUs                               │   │
│  │      • Use pinned memory for speed                                  │   │
│  │                                                                      │   │
│  │  3.5 RECREATE CUDA IPC HANDLES                                       │   │
│  │      • Old IPC handles are invalid                                  │   │
│  │      • Generate new handles for shared memory regions              │   │
│  │      • Update references in process memory                          │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 4: NCCL RESTORATION                                                   │
│  ══════════════════════════                                                  │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  4.1 COORDINATE RANKS                                                │   │
│  │      • All ranks must be restored before proceeding                 │   │
│  │      • Use shared memory barrier                                    │   │
│  │      • Or external coordinator (Redis, etcd)                        │   │
│  │                                                                      │   │
│  │  4.2 GENERATE NEW UNIQUE ID                                          │   │
│  │      • Rank 0 calls ncclGetUniqueId()                               │   │
│  │      • Broadcast to all ranks via coordinator                       │   │
│  │                                                                      │   │
│  │  4.3 REINITIALIZE COMMUNICATORS                                      │   │
│  │      • First NCCL call triggers reinit (via our intercept)         │   │
│  │      • ncclCommInitRank() with new ID, same rank/world_size        │   │
│  │      • Old (invalid) handles replaced with new ones                 │   │
│  │                                                                      │   │
│  │  4.4 VERIFY COMMUNICATION                                            │   │
│  │      • Quick all-reduce to verify connectivity                      │   │
│  │      • Detect any topology issues                                   │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                                                                              │
│  PHASE 5: RESUME                                                             │
│  ═══════════════                                                             │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │  5.1 UNFREEZE PROCESSES                                              │   │
│  │      • All processes continue execution                             │   │
│  │      • They think nothing happened                                  │   │
│  │                                                                      │   │
│  │  5.2 RECONNECT SERVICES                                              │   │
│  │      • API server listens on same port                              │   │
│  │      • Load balancer reconnects                                     │   │
│  │      • Health checks pass                                           │   │
│  │                                                                      │   │
│  │  5.3 VERIFY FUNCTIONALITY                                            │   │
│  │      • Send test inference request                                  │   │
│  │      • Verify output quality                                        │   │
│  │      • Check KV cache is working                                    │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Alternative Approaches

### Approach 1: External Checkpoint API (Ideal but blocked)

```text
┌─────────────────────────────────────────────────────────────────┐
│  NVIDIA cudaCheckpoint() API                                     │
│                                                                  │
│  If NVIDIA provided:                                             │
│    cudaCheckpoint(checkpoint_dir);                              │
│    cudaRestore(checkpoint_dir);                                 │
│                                                                  │
│  This would solve everything. But:                              │
│  • Not publicly available                                        │
│  • May exist internally at NVIDIA                               │
│  • Would require driver support                                  │
│                                                                  │
│  ACTION: Engage NVIDIA developer relations                      │
└─────────────────────────────────────────────────────────────────┘
```

### Approach 2: QEMU/KVM GPU Passthrough

```text
┌─────────────────────────────────────────────────────────────────┐
│  VM-Level Checkpoint                                             │
│                                                                  │
│  Run workload in VM with GPU passthrough, checkpoint entire VM  │
│                                                                  │
│  Pros:                                                           │
│  • Captures everything including GPU state                      │
│  • Works with any application                                   │
│                                                                  │
│  Cons:                                                           │
│  • Virtualization overhead                                       │
│  • Huge checkpoint size (entire VM memory)                      │
│  • Complex to integrate with Kubernetes                         │
│  • GPU passthrough compatibility issues                         │
│                                                                  │
│  VERDICT: Possible but not practical for production K8s        │
└─────────────────────────────────────────────────────────────────┘
```

### Approach 3: CUDA MPS (Multi-Process Service)

```text
┌─────────────────────────────────────────────────────────────────┐
│  CUDA MPS for centralized GPU access                            │
│                                                                  │
│  MPS server manages all GPU access:                             │
│  • Could potentially checkpoint MPS server                      │
│  • All clients go through single context                        │
│                                                                  │
│  Cons:                                                           │
│  • MPS is for GPU sharing, not checkpoint                       │
│  • Would still need to checkpoint client processes              │
│  • Not designed for this use case                               │
│                                                                  │
│  VERDICT: Not applicable                                        │
└─────────────────────────────────────────────────────────────────┘
```

### Approach 4: Application-Level (Violates constraint!)

```text
┌─────────────────────────────────────────────────────────────────┐
│  Modify vLLM to support checkpointing                           │
│                                                                  │
│  Add APIs to vLLM:                                               │
│    vllm.checkpoint(path)                                        │
│    vllm.restore(path)                                           │
│                                                                  │
│  This violates our "no app modification" constraint!            │
│                                                                  │
│  VERDICT: Not an option per requirements                        │
└─────────────────────────────────────────────────────────────────┘
```

### Our Approach: Hybrid System-Level

```text
┌─────────────────────────────────────────────────────────────────┐
│  NVSNAP HYBRID APPROACH                                           │
│                                                                  │
│  Combine:                                                        │
│  • CRIU for CPU state (proven technology)                       │
│  • LD_PRELOAD for CUDA/NCCL interception (no app changes)      │
│  • Direct GPU memory access for state capture                   │
│  • Coordinated multi-process protocol                           │
│                                                                  │
│  Why this works:                                                 │
│  • All interception is transparent to application               │
│  • No application code changes required                         │
│  • Leverages proven CRIU infrastructure                         │
│  • GPU state is reconstructable if we quiesce properly         │
│                                                                  │
│  VERDICT: This is our path                                      │
└─────────────────────────────────────────────────────────────────┘
```

---

## Implementation Roadmap

### Phase 4 Breakdown (vLLM Focus)

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                        PHASE 4: vLLM IMPLEMENTATION                          │
│                             (Weeks 29-40)                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  MILESTONE 4.1: Single-GPU vLLM (Weeks 29-31)                               │
│  ──────────────────────────────────────────────                             │
│  • Checkpoint vLLM with TP=1 (no NCCL)                                      │
│  • Handle PagedAttention KV cache                                           │
│  • Verify response quality post-restore                                     │
│  • Test with multiple model sizes (7B, 13B)                                │
│                                                                              │
│  Validation:                                                                 │
│  □ vLLM single-GPU checkpoint succeeds                                     │
│  □ Restore produces identical outputs                                       │
│  □ KV cache preserved (no quality degradation)                             │
│  □ Checkpoint time < 30s                                                    │
│                                                                              │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  MILESTONE 4.2: Multi-GPU vLLM with NCCL (Weeks 32-35)                      │
│  ───────────────────────────────────────────────────                        │
│  • Implement NCCL interception library                                      │
│  • Handle communicator reinitialization                                     │
│  • Coordinate checkpoint across ranks                                       │
│  • Test with TP=2, TP=4, TP=8                                              │
│                                                                              │
│  Validation:                                                                 │
│  □ TP=4 vLLM checkpoint succeeds                                           │
│  □ NCCL communication works post-restore                                   │
│  □ All-reduce produces correct results                                      │
│  □ Checkpoint time < 60s                                                    │
│                                                                              │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  MILESTONE 4.3: Request Handling (Weeks 36-37)                              │
│  ─────────────────────────────────────────────                              │
│  • Implement request draining                                               │
│  • Handle in-flight request checkpointing                                   │
│  • Test with continuous load                                                │
│  • Measure latency impact                                                   │
│                                                                              │
│  Validation:                                                                 │
│  □ Graceful drain before checkpoint                                        │
│  □ In-flight requests handled correctly                                    │
│  □ No request loss or duplication                                          │
│  □ Minimal latency spike during checkpoint                                 │
│                                                                              │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  MILESTONE 4.4: Spot Integration (Weeks 38-39)                              │
│  ─────────────────────────────────────────────                              │
│  • Integrate AWS/GCP spot termination signals                              │
│  • Implement checkpoint policies                                            │
│  • Automatic restore on new instance                                        │
│  • End-to-end spot migration demo                                          │
│                                                                              │
│  Validation:                                                                 │
│  □ Spot termination triggers checkpoint                                    │
│  □ Checkpoint completes within warning window                              │
│  □ Auto-restore on new instance                                            │
│  □ Total downtime < 5 minutes                                              │
│                                                                              │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  MILESTONE 4.5: Hardening (Week 40)                                         │
│  ──────────────────────────────────                                         │
│  • Edge case testing                                                        │
│  • Failure mode testing                                                     │
│  • Performance optimization                                                 │
│  • Documentation                                                            │
│                                                                              │
│  Validation:                                                                 │
│  □ All edge cases documented and handled                                   │
│  □ Failure recovery tested                                                  │
│  □ Performance meets targets                                               │
│  □ Documentation complete                                                   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Risk Analysis

### vLLM-Specific Risks

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| NCCL cannot reinitialize | Medium | Critical | Extensive testing, fallback to restart |
| GPU memory addresses differ | High | High | Pointer remapping implementation |
| KV cache corruption | Medium | High | Validation checksums, test coverage |
| vLLM internal changes | Medium | Medium | Pin vLLM versions, adapter pattern |
| Checkpoint too slow | Medium | Medium | Pre-copy, incremental checkpoints |
| CUDA version incompatibility | Low | High | Version matrix testing |

### Contingency Plans

```text
┌─────────────────────────────────────────────────────────────────┐
│  CONTINGENCY: NCCL Reinitialization Fails                       │
│                                                                  │
│  If NCCL cannot be reliably reinitialized:                      │
│                                                                  │
│  Plan A: Restart from checkpoint with fresh NCCL               │
│  • Restore CPU state only                                       │
│  • Let application reinitialize CUDA/NCCL                       │
│  • Reload model weights (cached locally)                        │
│  • Slower but reliable                                          │
│                                                                  │
│  Plan B: Sidecar approach                                       │
│  • Small sidecar handles NCCL initialization                   │
│  • Main process connects to sidecar                             │
│  • Sidecar can be restarted independently                      │
│                                                                  │
│  Plan C: Work with NVIDIA                                       │
│  • Engage NVIDIA developer relations                           │
│  • Request NCCL checkpoint support                             │
│  • Contribute to open-source NCCL                              │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  CONTINGENCY: GPU Address Mismatch                              │
│                                                                  │
│  If GPU allocations get different addresses on restore:         │
│                                                                  │
│  Plan A: Pointer remapping (implemented)                        │
│  • Scan CPU memory for GPU pointers                            │
│  • Build old→new address translation table                     │
│  • Update all pointers                                          │
│                                                                  │
│  Plan B: Reserved address space                                 │
│  • Modify LD_PRELOAD to reserve VA space at startup            │
│  • All allocations come from reserved region                   │
│  • Addresses are reproducible                                   │
│                                                                  │
│  Plan C: Same-GPU restore only                                  │
│  • Limit restore to same GPU model/index                       │
│  • Addresses likely to match                                    │
│  • Acceptable for spot recovery                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## Conclusion

Checkpointing vLLM without application modification is challenging but achievable. Our approach:

1. **Use CRIU** for CPU state (proven technology)
2. **LD_PRELOAD interception** for transparent CUDA/NCCL handling
3. **Coordinated multi-process protocol** for consistency
4. **Quiesce-based GPU capture** avoiding the need for driver support
5. **NCCL reinitialization** rather than checkpointing opaque state

The key insight is that we don't need to checkpoint *everything*—we need to checkpoint enough to reconstruct the application state. By letting the application reinitialize its CUDA/NCCL contexts but providing the right memory contents, we achieve transparency without deep driver integration.

---

*Document Version: 1.0*
*Last Updated: 2024*
*Authors: NVSNAP Team*

# NVSNAP Architecture Overview

## Executive Summary

NVSNAP is a production-ready GPU checkpoint/restore system for Kubernetes that enables live migration, spot instance resilience, and fast cold starts for GPU workloads including complex multi-process applications like vLLM.

### Design Principles

1. **Container Runtime Agnostic**: Operate at Linux process/namespace level, not container runtime APIs
2. **Application Transparent**: Zero modifications to applications - works with any CUDA/PyTorch/vLLM app
3. **GPU Vendor Agnostic**: Pluggable GPU backends (NVIDIA first, AMD/Intel later)
4. **Kubernetes Native**: Deep integration but version-independent
5. **Production Ready**: Comprehensive testing, observability, security

## The Hard Problem

GPU checkpoint/restore is fundamentally difficult because:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          WHY GPU C/R IS HARD                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. GPU STATE IS OPAQUE                                                     │
│     - CUDA contexts, streams, events managed by closed-source driver        │
│     - GPU memory not visible to kernel (can't use CRIU directly)            │
│     - No official NVIDIA checkpoint/restore API                             │
│                                                                             │
│  2. MULTI-PROCESS COMPLEXITY (vLLM, Megatron, etc.)                        │
│     - Multiple processes sharing GPU resources                              │
│     - NCCL communicators for inter-GPU communication                        │
│     - Shared memory regions between processes                               │
│     - Complex synchronization requirements                                   │
│                                                                             │
│  3. NETWORK STATE                                                           │
│     - NCCL uses sockets, shared memory, or RDMA                            │
│     - InfiniBand/RoCE connections can't be easily migrated                  │
│     - TCP connections in ESTABLISHED state                                  │
│                                                                             │
│  4. KERNEL OBJECTS                                                          │
│     - GPU-related file descriptors (/dev/nvidia*)                           │
│     - DMA mappings and pinned memory                                        │
│     - CUDA IPC handles                                                      │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Our Approach: Library Interposition + Process Coordination

Since we cannot modify applications and cannot rely on NVIDIA providing C/R support, we use a **library interposition** approach combined with **coordinated process checkpointing**:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        NVSNAP INTERPOSITION LAYER                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Application (vLLM, PyTorch, etc.)                                         │
│        │                                                                    │
│        ▼                                                                    │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                    NVSNAP Interception Layer                          │  │
│   │  (LD_PRELOAD libnvsnap.so - intercepts CUDA/NCCL/cuDNN calls)        │  │
│   │                                                                      │  │
│   │  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐                 │  │
│   │  │ CUDA Tracker │ │ NCCL Tracker │ │Memory Tracker│                 │  │
│   │  │              │ │              │ │              │                 │  │
│   │  │ - Contexts   │ │ - Comms      │ │ - Allocations│                 │  │
│   │  │ - Streams    │ │ - Ranks      │ │ - Mappings   │                 │  │
│   │  │ - Events     │ │ - Operations │ │ - Pins       │                 │  │
│   │  └──────────────┘ └──────────────┘ └──────────────┘                 │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│        │                                                                    │
│        ▼                                                                    │
│   Real CUDA/NCCL Libraries                                                  │
│        │                                                                    │
│        ▼                                                                    │
│   NVIDIA Driver + GPU Hardware                                              │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### How It Works

**During Normal Execution:**
1. Interception layer tracks all CUDA API calls (allocations, contexts, streams)
2. Tracks NCCL communicator setup and operations
3. Maintains shadow state that mirrors GPU state
4. Zero overhead for most operations (just pointer forwarding)

**During Checkpoint:**
1. Coordinate all processes in the workload (barrier)
2. Drain in-flight NCCL operations
3. Synchronize all CUDA streams
4. Dump GPU memory via cuMemcpy to host
5. Serialize tracked state (contexts, streams, allocations map)
6. Checkpoint CPU state with CRIU
7. Package everything together

**During Restore:**
1. Restore CPU state with CRIU (process frozen)
2. Reinitialize CUDA contexts using tracked state
3. Reallocate GPU memory at same virtual addresses (or remap)
4. Restore GPU memory contents
5. Reinitialize NCCL communicators
6. Resume execution

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           NVSNAP SYSTEM ARCHITECTURE                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                         CONTROL PLANE                                │   │
│  │                                                                      │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐   │   │
│  │  │ API Server   │  │  Controller  │  │  Orchestrator            │   │   │
│  │  │              │  │              │  │                          │   │   │
│  │  │ - gRPC/REST  │  │ - CRD Watch  │  │ - Multi-node coord       │   │   │
│  │  │ - Auth/AuthZ │  │ - Reconcile  │  │ - Cross-cluster migrate  │   │   │
│  │  │ - Validation │  │ - Scheduling │  │ - Policy enforcement     │   │   │
│  │  └──────────────┘  └──────────────┘  └──────────────────────────┘   │   │
│  │                           │                                          │   │
│  │                           │ K8s API                                  │   │
│  └───────────────────────────┼──────────────────────────────────────────┘   │
│                              │                                              │
│  ┌───────────────────────────┼──────────────────────────────────────────┐   │
│  │                           ▼                                          │   │
│  │                      DATA PLANE (per node)                           │   │
│  │                                                                      │   │
│  │  ┌──────────────────────────────────────────────────────────────┐   │   │
│  │  │                     NODE AGENT                                │   │   │
│  │  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐   │   │   │
│  │  │  │Process Mgr  │  │ C/R Engine  │  │ Storage Client      │   │   │   │
│  │  │  │             │  │             │  │                     │   │   │   │
│  │  │  │- Discovery  │  │- CRIU       │  │- S3/GCS/NFS        │   │   │   │
│  │  │  │- Namespaces │  │- GPU C/R    │  │- Streaming         │   │   │   │
│  │  │  │- Cgroups    │  │- Coord      │  │- Dedup             │   │   │   │
│  │  │  └─────────────┘  └─────────────┘  └─────────────────────┘   │   │   │
│  │  └──────────────────────────────────────────────────────────────┘   │   │
│  │                              │                                       │   │
│  │                              ▼                                       │   │
│  │  ┌──────────────────────────────────────────────────────────────┐   │   │
│  │  │              INTERCEPTION LAYER (libnvsnap.so)                 │   │   │
│  │  │                                                               │   │   │
│  │  │   Loaded via LD_PRELOAD into GPU containers                   │   │   │
│  │  │   Tracks CUDA/NCCL state, enables transparent C/R             │   │   │
│  │  └──────────────────────────────────────────────────────────────┘   │   │
│  │                                                                      │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Why This Approach Works

### Container Runtime Independence

We don't use containerd or CRI-O APIs. Instead:

| What We Need | Traditional Approach | Our Approach |
|--------------|---------------------|--------------|
| Find container processes | Container runtime API | `/proc` + cgroup inspection |
| Get container namespaces | Runtime metadata | `/proc/[pid]/ns/*` |
| Pause/resume | Runtime pause API | `SIGSTOP`/`SIGCONT` or freezer cgroup |
| Get environment | Runtime inspect | `/proc/[pid]/environ` |
| Get mounts | Runtime inspect | `/proc/[pid]/mounts` |

### Application Transparency

The interception layer is injected via environment variable:
```yaml
env:
  - name: LD_PRELOAD
    value: /opt/nvsnap/lib/libnvsnap.so
```

This is set by our mutating webhook - the application image is never modified.

### vLLM and Complex Workloads

For multi-process workloads:

1. **Process Group Detection**: Identify all processes belonging to a workload via:
   - Parent-child relationships
   - Shared namespaces
   - NCCL communicator membership (tracked by interception layer)

2. **Coordinated Checkpoint**:
   ```
   Process 1 ──┐
   Process 2 ──┼──► Barrier ──► Drain NCCL ──► Sync CUDA ──► Checkpoint All
   Process 3 ──┘
   ```

3. **NCCL State Reconstruction**:
   - We track `ncclCommInitRank` calls (rank, nranks, unique ID)
   - On restore, we reinitialize with same parameters
   - NCCL handles reconnection automatically

## Next Steps

See the following documents for detailed information:
- [03-INTERCEPTION-LAYER.md](03-INTERCEPTION-LAYER.md) - Deep dive on CUDA/NCCL interception
- [04-CHECKPOINT-ENGINE.md](04-CHECKPOINT-ENGINE.md) - Checkpoint/restore engine design
- [05-MULTI-PROCESS.md](05-MULTI-PROCESS.md) - Multi-process coordination for vLLM
- [06-STORAGE.md](06-STORAGE.md) - Storage backend architecture
- [07-KUBERNETES.md](07-KUBERNETES.md) - Kubernetes integration

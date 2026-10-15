# NVSNAP Architecture Document

## Executive Summary

NVSNAP is a production-grade GPU checkpoint/restore system for Kubernetes that enables live migration of GPU workloads without application modification. This document outlines the architecture, technical challenges, and phased implementation plan.

**Key Constraints:**
- ❌ Cannot modify applications (PyTorch, vLLM, etc.)
- ❌ Cannot depend on specific container runtimes (containerd, CRI-O)
- ❌ Cannot depend on specific Kubernetes versions
- ✅ Must handle complex multi-process GPU workloads (vLLM, DeepSpeed, Megatron)
- ✅ Must be production-ready with comprehensive testing
- ✅ Must support NVIDIA GPUs (AMD/Intel future)

---

## Table of Contents

1. [The Hard Problems](#the-hard-problems)
2. [Architecture Overview](#architecture-overview)
3. [Core Technical Approach](#core-technical-approach)
4. [Component Deep Dive](#component-deep-dive)
5. [Multi-Process GPU Workloads (vLLM)](#multi-process-gpu-workloads-vllm)
6. [Implementation Phases](#implementation-phases)
7. [Testing Strategy](#testing-strategy)

---

## The Hard Problems

### Problem 1: GPU State is Not in Process Memory

Unlike CPU applications where all state lives in process memory (checkpointable via CRIU), GPU applications have state in multiple places:

```
┌─────────────────────────────────────────────────────────────────┐
│                     GPU Application State                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────────────────────┐  │
│  │   CPU Memory     │     │          GPU Memory              │  │
│  │  (CRIU can       │     │  (Tensors, KV Cache, Activations)│  │
│  │   checkpoint)    │     │  NOT in process address space    │  │
│  └──────────────────┘     └──────────────────────────────────┘  │
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────────────────────┐  │
│  │  CUDA Runtime    │     │       CUDA Driver State          │  │
│  │  State (contexts,│     │  (Kernel modules, device state)  │  │
│  │  streams, events)│     │  Kernel-level, not accessible    │  │
│  └──────────────────┘     └──────────────────────────────────┘  │
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────────────────────┐  │
│  │  File Descriptors│     │     IPC State (NCCL, MPI)        │  │
│  │  (GPU device fds)│     │  Shared memory, sockets, etc.    │  │
│  └──────────────────┘     └──────────────────────────────────┘  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Problem 2: NVIDIA Doesn't Provide Checkpoint APIs

NVIDIA does not provide public APIs for:
- Dumping GPU memory contents
- Saving CUDA context state
- Serializing stream/event state
- Restoring any of the above

**Our Approach:** We must work around this limitation using:
1. **CUDA Driver API** - Low-level access to GPU memory
2. **Library Interposition** - Track CUDA calls without modifying apps
3. **Process Introspection** - Understand GPU state from /proc and nvidia-smi

### Problem 3: Multi-Process Coordination (vLLM/DeepSpeed)

vLLM and similar frameworks use:
- Multiple worker processes
- NCCL for GPU-to-GPU communication
- Shared memory for IPC
- Complex process hierarchies

```
┌─────────────────────────────────────────────────────────────────┐
│                    vLLM Process Hierarchy                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                   Main Process (API Server)              │    │
│  │                   - HTTP endpoint                        │    │
│  │                   - Request scheduling                   │    │
│  └─────────────────────────┬───────────────────────────────┘    │
│                            │                                     │
│           ┌────────────────┼────────────────┐                   │
│           ▼                ▼                ▼                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐              │
│  │  Worker 0   │  │  Worker 1   │  │  Worker N   │              │
│  │  GPU 0      │  │  GPU 1      │  │  GPU N      │              │
│  │             │  │             │  │             │              │
│  │  - Model    │  │  - Model    │  │  - Model    │              │
│  │    shard    │  │    shard    │  │    shard    │              │
│  │  - KV Cache │  │  - KV Cache │  │  - KV Cache │              │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘              │
│         │                │                │                      │
│         └────────────────┼────────────────┘                      │
│                          ▼                                       │
│              ┌───────────────────────┐                          │
│              │    NCCL Communicator  │                          │
│              │  - AllReduce          │                          │
│              │  - AllGather          │                          │
│              │  - P2P transfers      │                          │
│              └───────────────────────┘                          │
│                                                                  │
│  Shared Resources:                                               │
│  - /dev/shm segments                                            │
│  - Unix domain sockets                                          │
│  - CUDA IPC handles                                             │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Problem 4: Container Runtime Independence

We cannot use containerd or CRI-O checkpoint APIs because:
1. Not all runtimes support checkpoint
2. Runtime versions vary across clusters
3. Checkpoint implementations differ
4. GPU support in runtime checkpoint is limited/non-existent

**Our Approach:** Work at the Linux process level, not container level.

---

## Architecture Overview

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              NVSNAP System                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                         Control Plane                                   │ │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐  │ │
│  │  │  API Server  │  │  Controller  │  │   Scheduler  │  │  Storage   │  │ │
│  │  │  (gRPC/REST) │  │  (K8s CRDs)  │  │  (Placement) │  │  Manager   │  │ │
│  │  └──────────────┘  └──────────────┘  └──────────────┘  └────────────┘  │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                    │                                         │
│                                    ▼                                         │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                    Data Plane (DaemonSet per Node)                      │ │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │ │
│  │  │                        Node Agent                                 │  │ │
│  │  │  ┌────────────────┐  ┌────────────────┐  ┌────────────────────┐  │  │ │
│  │  │  │ Process        │  │ GPU Checkpoint │  │ Namespace/Cgroup   │  │  │ │
│  │  │  │ Coordinator    │  │ Engine         │  │ Manager            │  │  │ │
│  │  │  └────────────────┘  └────────────────┘  └────────────────────┘  │  │ │
│  │  │  ┌────────────────┐  ┌────────────────┐  ┌────────────────────┐  │  │ │
│  │  │  │ CRIU           │  │ CUDA           │  │ Storage            │  │  │ │
│  │  │  │ Integration    │  │ Interceptor    │  │ Streamer           │  │  │ │
│  │  │  └────────────────┘  └────────────────┘  └────────────────────┘  │  │ │
│  │  └──────────────────────────────────────────────────────────────────┘  │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Process-level operation** | Runtime agnostic - we work with Linux processes directly |
| **Namespace-aware** | Understand container boundaries without runtime dependency |
| **Library interposition** | Track CUDA state without app modification |
| **Coordinated multi-process** | Handle vLLM/DeepSpeed process groups |
| **Streaming checkpoint** | Large GPU memory (80GB+) requires streaming |
| **Incremental checkpoint** | Reduce checkpoint time for frequent checkpoints |

---

## Core Technical Approach

### Layer 1: Process Discovery (Runtime Agnostic)

Instead of asking containerd/CRI-O about containers, we discover processes directly:

```go
// ProcessDiscovery finds GPU processes without runtime dependency
type ProcessDiscovery struct {
    // We use /proc filesystem directly
    procFS string
    
    // nvidia-smi for GPU process mapping
    nvidiaSmi string
}

func (pd *ProcessDiscovery) FindGPUProcesses(ctx context.Context) ([]*GPUProcess, error) {
    // 1. Query nvidia-smi for processes using GPUs
    // 2. Read /proc/<pid>/cgroup to find container cgroup
    // 3. Read /proc/<pid>/ns/* to understand namespace membership
    // 4. Correlate with Kubernetes pod info via cgroup naming
    
    // This works regardless of containerd, CRI-O, or any runtime
}
```

**How we map processes to pods without runtime APIs:**

```
┌─────────────────────────────────────────────────────────────────┐
│                   Process to Pod Mapping                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. nvidia-smi --query-compute-apps=pid → GPU PIDs              │
│                                                                  │
│  2. /proc/<pid>/cgroup → Container cgroup path                  │
│     Example: /kubepods/pod<uid>/<container-id>                  │
│                                                                  │
│  3. Parse cgroup path → Extract pod UID and container ID        │
│                                                                  │
│  4. Kubernetes API → Map pod UID to pod metadata                │
│                                                                  │
│  Result: Full pod context without touching container runtime     │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Layer 2: CUDA State Tracking (Library Interposition)

We use `LD_PRELOAD` to intercept CUDA calls and track state:

```
┌─────────────────────────────────────────────────────────────────┐
│                   CUDA Interposition Layer                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Application                                                     │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │              libnvsnap_intercept.so                       │    │
│  │                                                          │    │
│  │  Intercepts:                   Records:                  │    │
│  │  - cuMemAlloc()               - Allocation address/size  │    │
│  │  - cuMemFree()                - Deallocation             │    │
│  │  - cuStreamCreate()           - Stream handles           │    │
│  │  - cuEventCreate()            - Event handles            │    │
│  │  - cuModuleLoad()             - Loaded modules           │    │
│  │  - cuLaunchKernel()           - Kernel launches          │    │
│  │  - cudaMalloc()               - Runtime allocations      │    │
│  │  - cudaMemcpy*()              - Memory state hints       │    │
│  │                                                          │    │
│  │  Communicates with agent via:                            │    │
│  │  - Unix domain socket                                    │    │
│  │  - Shared memory (for speed)                             │    │
│  │                                                          │    │
│  └──────────────────────────┬──────────────────────────────┘    │
│                             │                                    │
│                             ▼                                    │
│                    Real CUDA Libraries                           │
│                    (libcuda.so, libcudart.so)                   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Critical Insight:** The interception library is injected at container start via a mutating webhook that adds `LD_PRELOAD`. This is NOT application modification - it's container configuration.

### Layer 3: GPU Memory Checkpoint

Since NVIDIA doesn't provide checkpoint APIs, we use the CUDA Driver API:

```c
// GPU Memory Dump Strategy
//
// 1. Pause all GPU operations (freeze CUDA contexts)
// 2. For each tracked allocation:
//    - cuMemcpyDtoH() to copy GPU memory to CPU
//    - Record allocation metadata (size, flags, etc.)
// 3. Serialize to checkpoint format
// 4. Resume GPU operations

typedef struct {
    CUdeviceptr device_ptr;     // GPU address
    size_t size;                 // Allocation size
    unsigned int flags;          // Allocation flags
    void* host_copy;            // CPU copy of data
} GPUAllocation;

int checkpoint_gpu_memory(pid_t pid, GPUAllocation** allocs, int* count) {
    // Attach to process's CUDA context
    // Iterate tracked allocations
    // Copy each to host memory
    // This is the core of GPU checkpoint
}
```

### Layer 4: Process Checkpoint (CRIU-based)

We use CRIU for CPU state but with custom handling for GPU:

```
┌─────────────────────────────────────────────────────────────────┐
│                   Checkpoint Flow                                │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. FREEZE all processes in the process group                   │
│     └── Uses cgroup freezer or SIGSTOP                          │
│                                                                  │
│  2. CHECKPOINT GPU STATE                                         │
│     ├── Query interception library for tracked state            │
│     ├── Dump GPU memory via CUDA Driver API                     │
│     ├── Record context/stream/event state                       │
│     └── Save NCCL communicator info (for multi-GPU)             │
│                                                                  │
│  3. CHECKPOINT CPU STATE (CRIU)                                  │
│     ├── Process memory                                           │
│     ├── File descriptors (excluding GPU devices)                │
│     ├── Network connections (TCP established)                   │
│     ├── IPC (shared memory, semaphores)                         │
│     └── Namespaces state                                         │
│                                                                  │
│  4. STREAM to storage                                            │
│     └── Parallel streaming of CPU + GPU checkpoint data         │
│                                                                  │
│  5. UNFREEZE (if leave-running mode)                            │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Multi-Process GPU Workloads (vLLM)

This is the hardest part. vLLM uses:
- Ray or multiprocessing for worker management
- NCCL for GPU collective operations
- Shared memory for tensor passing
- Complex process hierarchies

### The vLLM Checkpoint Challenge

```
┌─────────────────────────────────────────────────────────────────┐
│              vLLM Checkpoint Challenges                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Challenge 1: NCCL Communicator State                           │
│  ─────────────────────────────────────                          │
│  NCCL communicators contain:                                     │
│  - Unique IDs that must match across processes                  │
│  - Internal connection state (sockets, shared memory)           │
│  - Ring/tree topology information                               │
│                                                                  │
│  Solution: We don't checkpoint NCCL state directly.             │
│  Instead, we:                                                    │
│  1. Quiesce NCCL operations (wait for completion)               │
│  2. Destroy communicators cleanly                               │
│  3. Record the NCCL unique ID and topology                      │
│  4. On restore, reinitialize NCCL with same unique ID           │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  Challenge 2: Process Coordination                               │
│  ────────────────────────────────                                │
│  All workers must checkpoint atomically:                        │
│  - If one fails, all must abort                                 │
│  - Checkpoint must capture consistent state                     │
│                                                                  │
│  Solution: Two-Phase Checkpoint Protocol                        │
│  1. PREPARE: All processes acknowledge ready                    │
│  2. COMMIT: All processes checkpoint simultaneously             │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  Challenge 3: Shared Memory                                      │
│  ───────────────────────────                                     │
│  Workers share tensors via /dev/shm                             │
│                                                                  │
│  Solution: Checkpoint shared memory regions with                │
│  ownership tracking. Only checkpoint once, restore              │
│  with same addresses.                                            │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  Challenge 4: GPU-to-GPU (P2P) Memory                           │
│  ────────────────────────────────────                            │
│  GPUs may have direct P2P mappings                              │
│                                                                  │
│  Solution: Disable P2P before checkpoint, re-enable             │
│  after restore. P2P mappings are recreated automatically.       │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Multi-Process Checkpoint Protocol

```
┌─────────────────────────────────────────────────────────────────┐
│           Coordinated Multi-Process Checkpoint                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Agent                 Process Group (vLLM workers)              │
│    │                   P0        P1        P2        P3          │
│    │                   │         │         │         │           │
│    │──PREPARE────────►│         │         │         │           │
│    │                   │◄───────►│◄───────►│◄───────►│           │
│    │                   │   (barrier sync via NCCL)   │           │
│    │                   │         │         │         │           │
│    │◄──READY──────────│         │         │         │           │
│    │◄──READY──────────│─────────│         │         │           │
│    │◄──READY──────────│─────────│─────────│         │           │
│    │◄──READY──────────│─────────│─────────│─────────│           │
│    │                   │         │         │         │           │
│    │                   │   Quiesce NCCL operations   │           │
│    │                   │   Destroy communicators     │           │
│    │                   │         │         │         │           │
│    │──FREEZE─────────►│         │         │         │           │
│    │                   ├─────────┼─────────┼─────────┤           │
│    │                   │    All processes frozen     │           │
│    │                   ├─────────┼─────────┼─────────┤           │
│    │                   │         │         │         │           │
│    │──CHECKPOINT──────►│        │         │         │           │
│    │                   │  ┌─────┴────┐    │         │           │
│    │                   │  │GPU+CPU   │    │         │           │
│    │                   │  │Checkpoint│    │         │           │
│    │                   │  └─────┬────┘    │         │           │
│    │                   │         │         │         │           │
│    │                   │ (parallel checkpoint of all processes) │
│    │                   │         │         │         │           │
│    │◄──COMPLETE───────│─────────│─────────│─────────│           │
│    │                   │         │         │         │           │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### NCCL Quiesce and Restore Strategy

```python
# Conceptual flow for NCCL handling

def checkpoint_nccl_workload(process_group):
    """
    Strategy for checkpointing NCCL-based workloads like vLLM
    """
    
    # Phase 1: Quiesce
    # ─────────────────
    # We inject a "quiesce" signal into each process
    # The interception library catches the next NCCL call
    # and performs a barrier + communicator save
    
    for proc in process_group:
        inject_quiesce_signal(proc)
    
    # Wait for all processes to reach quiesce point
    wait_for_quiesce_barrier(process_group)
    
    # At this point:
    # - All NCCL operations are complete
    # - Communicator state is saved (unique ID, rank, size)
    # - Communicators are destroyed cleanly
    
    # Phase 2: Checkpoint
    # ────────────────────
    # Now we can safely checkpoint GPU + CPU state
    
    checkpoint_data = parallel_checkpoint(process_group)
    
    return checkpoint_data


def restore_nccl_workload(checkpoint_data, target_gpus):
    """
    Strategy for restoring NCCL-based workloads
    """
    
    # Phase 1: Restore processes in frozen state
    # ───────────────────────────────────────────
    
    restored_procs = []
    for proc_ckpt in checkpoint_data.processes:
        proc = restore_process_frozen(proc_ckpt, target_gpus)
        restored_procs.append(proc)
    
    # Phase 2: Reinitialize NCCL
    # ──────────────────────────
    # The interception library will intercept the first NCCL call
    # and reinitialize with the saved unique ID
    
    for proc in restored_procs:
        inject_nccl_reinit_config(proc, checkpoint_data.nccl_state)
    
    # Phase 3: Resume
    # ───────────────
    # Unfreeze all processes simultaneously
    
    parallel_resume(restored_procs)
    
    # The processes will:
    # 1. Resume execution
    # 2. Hit their first NCCL call
    # 3. Interception library reinitializes NCCL
    # 4. Continue as normal
```

---

## Implementation Phases

### Phase 0: Foundation (Weeks 1-4)

**Goal:** Establish project infrastructure and core abstractions.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 0 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Project Structure                                            │
│    ├── Go module with proper dependency management              │
│    ├── Makefile with build, test, lint targets                  │
│    ├── Docker build infrastructure                              │
│    └── CI/CD pipeline (GitHub Actions)                          │
│                                                                  │
│  ☐ Core Abstractions                                            │
│    ├── Process discovery interface                              │
│    ├── GPU engine interface                                     │
│    ├── Storage interface                                         │
│    ├── Checkpoint/Restore interfaces                            │
│    └── Agent communication protocol (gRPC)                      │
│                                                                  │
│  ☐ Kubernetes CRDs                                              │
│    ├── Checkpoint CRD                                           │
│    ├── Restore CRD                                              │
│    ├── NVSNAPNode CRD                                            │
│    └── CheckpointPolicy CRD                                     │
│                                                                  │
│  ☐ Testing Infrastructure                                       │
│    ├── Unit test framework                                      │
│    ├── Integration test framework                               │
│    ├── Kind cluster for local testing                           │
│    └── Mock GPU environment                                     │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ make build succeeds                                          │
│  ✓ make test passes with >80% coverage                         │
│  ✓ CRDs install on kind cluster                                 │
│  ✓ Basic controller reconciliation works                        │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 1: Single-Process CPU Checkpoint (Weeks 5-8)

**Goal:** Checkpoint and restore a single containerized process (no GPU).

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 1 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Process Discovery                                            │
│    ├── Find processes by cgroup/namespace                       │
│    ├── Map to Kubernetes pods                                   │
│    └── Unit tests for discovery                                 │
│                                                                  │
│  ☐ CRIU Integration                                             │
│    ├── CRIU wrapper library                                     │
│    ├── Checkpoint to local filesystem                           │
│    ├── Restore from local filesystem                            │
│    └── Handle namespaces correctly                              │
│                                                                  │
│  ☐ Node Agent (Basic)                                           │
│    ├── gRPC server                                              │
│    ├── Checkpoint endpoint                                      │
│    ├── Restore endpoint                                         │
│    └── Status reporting                                         │
│                                                                  │
│  ☐ Controller (Basic)                                           │
│    ├── Reconcile Checkpoint resources                           │
│    ├── Communicate with agent                                   │
│    └── Update status                                            │
│                                                                  │
│  Test Workload: Simple Python script in a container             │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint a running Python container                        │
│  ✓ Restore on same node                                         │
│  ✓ Process resumes execution correctly                          │
│  ✓ Works with both containerd and CRI-O                        │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Cluster Test Plan:**
```bash
# Deploy test workload
kubectl apply -f test/workloads/simple-python.yaml

# Create checkpoint
kubectl apply -f - <<EOF
apiVersion: nvsnap.io/v1alpha1
kind: Checkpoint
metadata:
  name: test-ckpt-1
spec:
  target:
    kind: Pod
    name: simple-python
  storage:
    type: local
    path: /var/lib/nvsnap/checkpoints
EOF

# Wait for completion
kubectl wait --for=condition=Complete checkpoint/test-ckpt-1

# Delete original pod
kubectl delete pod simple-python

# Restore
kubectl apply -f - <<EOF
apiVersion: nvsnap.io/v1alpha1
kind: Restore
metadata:
  name: test-restore-1
spec:
  checkpoint: test-ckpt-1
EOF

# Verify process resumed correctly
kubectl logs <restored-pod> | grep "resumed"
```

### Phase 2: Single-Process GPU Checkpoint (Weeks 9-14)

**Goal:** Checkpoint and restore a single-process GPU application (simple CUDA).

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 2 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ CUDA Interception Library (libnvsnap_intercept.so)           │
│    ├── Intercept cuMemAlloc/cuMemFree                          │
│    ├── Track allocations in shared memory                       │
│    ├── Communication with agent                                 │
│    └── Minimal performance overhead (<1%)                       │
│                                                                  │
│  ☐ GPU Memory Checkpoint                                        │
│    ├── Iterate tracked allocations                              │
│    ├── cuMemcpyDtoH for each allocation                        │
│    ├── Parallel memory copy for large allocations              │
│    └── Compression (zstd)                                       │
│                                                                  │
│  ☐ GPU Memory Restore                                           │
│    ├── Allocate GPU memory                                      │
│    ├── cuMemcpyHtoD to restore data                            │
│    ├── Handle allocation failures gracefully                   │
│    └── Verify restored state                                    │
│                                                                  │
│  ☐ Mutating Webhook                                             │
│    ├── Inject LD_PRELOAD for GPU containers                    │
│    ├── Mount interception library                               │
│    └── Add agent communication volume                           │
│                                                                  │
│  Test Workload: Simple CUDA vector addition                     │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint running CUDA program                              │
│  ✓ GPU memory correctly saved                                   │
│  ✓ Restore on same GPU type                                     │
│  ✓ Computation continues correctly                              │
│  ✓ Memory contents verified                                     │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Test Program:**
```cuda
// test_cuda_checkpoint.cu
// Performs iterative computation, checkpointable at any iteration

__global__ void iterate_kernel(float* data, int n, int iteration) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx < n) {
        data[idx] = data[idx] * 1.01f + (float)iteration * 0.001f;
    }
}

int main() {
    float* d_data;
    cudaMalloc(&d_data, N * sizeof(float));
    
    for (int i = 0; ; i++) {
        iterate_kernel<<<blocks, threads>>>(d_data, N, i);
        cudaDeviceSynchronize();
        
        // Log progress (for verification after restore)
        printf("Iteration %d complete\n", i);
        sleep(1);
    }
}
```

### Phase 3: PyTorch Checkpoint (Weeks 15-20)

**Goal:** Checkpoint and restore PyTorch training jobs.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 3 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Enhanced CUDA Interception                                   │
│    ├── cudaMalloc/cudaFree (runtime API)                       │
│    ├── cudaMemcpy* operations                                   │
│    ├── cuDNN state tracking                                     │
│    └── cuBLAS handle tracking                                   │
│                                                                  │
│  ☐ PyTorch-Specific Handling                                    │
│    ├── Tensor allocator integration                             │
│    ├── Autograd graph (not checkpointed - recomputed)          │
│    ├── DataLoader state (file positions, shuffle state)        │
│    └── Random number generator state                           │
│                                                                  │
│  ☐ Storage Backend                                              │
│    ├── S3 streaming upload/download                             │
│    ├── Parallel multipart upload                                │
│    ├── Checksum verification                                    │
│    └── Encryption support                                       │
│                                                                  │
│  Test Workload: ResNet-50 training on ImageNet                  │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint during training iteration                         │
│  ✓ Restore and continue training                                │
│  ✓ Loss curve continuous (no discontinuity)                    │
│  ✓ Final accuracy matches non-checkpointed run                 │
│  ✓ Works with DDP disabled                                      │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 4: Multi-Process Coordination (Weeks 21-28)

**Goal:** Checkpoint and restore multi-process GPU workloads (DDP, no NCCL yet).

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 4 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Process Group Discovery                                      │
│    ├── Identify related processes (same pod, same cgroup)      │
│    ├── Build process tree                                       │
│    ├── Track IPC relationships                                  │
│    └── Shared memory mapping                                    │
│                                                                  │
│  ☐ Coordinated Checkpoint                                       │
│    ├── Two-phase commit protocol                                │
│    ├── Atomic group checkpoint                                  │
│    ├── Failure handling (abort all on any failure)             │
│    └── Shared memory deduplication                              │
│                                                                  │
│  ☐ Coordinated Restore                                          │
│    ├── Restore all processes frozen                             │
│    ├── Re-establish IPC channels                                │
│    ├── Synchronize resume                                       │
│    └── Verify inter-process communication                       │
│                                                                  │
│  Test Workload: PyTorch DDP with Gloo backend (no NCCL)        │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint multi-process training job                        │
│  ✓ All processes checkpointed atomically                       │
│  ✓ Restore all processes                                        │
│  ✓ DDP continues correctly                                      │
│  ✓ Gradients synchronized correctly post-restore               │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 5: NCCL Support (Weeks 29-36)

**Goal:** Full NCCL support for production multi-GPU training.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 5 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ NCCL Interception                                            │
│    ├── ncclCommInitRank interception                            │
│    ├── Track communicator unique IDs                            │
│    ├── Track rank and world size                                │
│    └── Collective operation tracking                            │
│                                                                  │
│  ☐ NCCL Quiesce Protocol                                        │
│    ├── Drain in-flight operations                               │
│    ├── Barrier synchronization                                  │
│    ├── Clean communicator teardown                              │
│    └── State serialization                                      │
│                                                                  │
│  ☐ NCCL Restore                                                 │
│    ├── Communicator reinitialization                            │
│    ├── Same unique ID reuse                                     │
│    ├── Topology reconstruction                                  │
│    └── Verify collective operations work                        │
│                                                                  │
│  Test Workload: PyTorch DDP with NCCL backend                  │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint 4-GPU DDP training                                │
│  ✓ NCCL operations quiesce cleanly                              │
│  ✓ Restore with NCCL reinitialized                             │
│  ✓ AllReduce works correctly post-restore                      │
│  ✓ Training continues to convergence                           │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 6: vLLM Support (Weeks 37-48)

**Goal:** Full support for vLLM inference workloads.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 6 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ vLLM Process Analysis                                        │
│    ├── Map vLLM process architecture                            │
│    ├── Identify all worker processes                            │
│    ├── Track Ray/multiprocessing relationships                  │
│    └── Understand KV cache management                           │
│                                                                  │
│  ☐ vLLM-Specific Checkpoint                                     │
│    ├── KV cache checkpoint                                      │
│    ├── Request queue state                                      │
│    ├── Token buffer state                                       │
│    └── Scheduler state                                          │
│                                                                  │
│  ☐ vLLM Restore                                                 │
│    ├── Worker process restoration                               │
│    ├── NCCL reinit for tensor parallel                         │
│    ├── KV cache restoration                                     │
│    └── In-flight request handling                               │
│                                                                  │
│  ☐ Live Migration                                               │
│    ├── Cross-node migration support                             │
│    ├── Different GPU topology handling                          │
│    └── Minimal request latency impact                           │
│                                                                  │
│  Test Workload: vLLM with Llama-2-70B (8 GPU tensor parallel)  │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ Checkpoint during active inference                           │
│  ✓ In-flight requests preserved or gracefully failed           │
│  ✓ Restore on different node                                    │
│  ✓ Inference continues correctly                                │
│  ✓ KV cache validated                                           │
│  ✓ Performance restored to pre-checkpoint levels               │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 7: Production Hardening (Weeks 49-56)

**Goal:** Production readiness with comprehensive testing.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 7 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Reliability                                                  │
│    ├── Chaos testing (process crashes, network partitions)     │
│    ├── Resource exhaustion handling                             │
│    ├── Partial failure recovery                                 │
│    └── Idempotent operations                                    │
│                                                                  │
│  ☐ Performance                                                  │
│    ├── Checkpoint time < 30s for 80GB GPU memory               │
│    ├── Minimal application impact during checkpoint             │
│    ├── Incremental checkpoint support                           │
│    └── Streaming to avoid memory pressure                       │
│                                                                  │
│  ☐ Security                                                     │
│    ├── Encryption at rest                                       │
│    ├── Encryption in transit                                    │
│    ├── RBAC for checkpoint operations                           │
│    ├── Audit logging                                            │
│    └── Secure credential handling                               │
│                                                                  │
│  ☐ Observability                                                │
│    ├── Prometheus metrics                                       │
│    ├── OpenTelemetry tracing                                    │
│    ├── Structured logging                                       │
│    └── Alerting rules                                           │
│                                                                  │
│  ☐ Documentation                                                │
│    ├── Architecture documentation                               │
│    ├── API reference                                            │
│    ├── Operations guide                                         │
│    ├── Troubleshooting guide                                    │
│    └── Security considerations                                  │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ 99.9% checkpoint success rate under load                    │
│  ✓ No data corruption in 10,000 checkpoint/restore cycles     │
│  ✓ Security audit passed                                        │
│  ✓ Performance benchmarks met                                   │
│  ✓ Documentation complete                                       │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Phase 8: Web UI & Polish (Weeks 57-60)

**Goal:** Beautiful, functional web UI.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Phase 8 Deliverables                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ☐ Web UI                                                       │
│    ├── Dashboard with cluster overview                          │
│    ├── Checkpoint list and management                           │
│    ├── Real-time checkpoint/restore progress                    │
│    ├── GPU utilization visualization                            │
│    ├── Policy management                                        │
│    └── Audit log viewer                                         │
│                                                                  │
│  ☐ CLI Tool                                                     │
│    ├── nvsnapctl checkpoint create                               │
│    ├── nvsnapctl checkpoint list                                 │
│    ├── nvsnapctl restore create                                  │
│    ├── nvsnapctl status                                          │
│    └── Shell completions                                        │
│                                                                  │
│  ☐ Helm Chart                                                   │
│    ├── Production-ready values                                  │
│    ├── HA deployment options                                    │
│    ├── Resource limits/requests                                 │
│    └── Upgrade procedures                                       │
│                                                                  │
│  Validation Criteria:                                           │
│  ✓ UI usability tested with real users                         │
│  ✓ All operations accessible via UI                            │
│  ✓ CLI feature parity with API                                 │
│  ✓ Helm install works on GKE, EKS, AKS                        │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Testing Strategy

### Test Pyramid

```
                    ┌─────────────────┐
                    │     E2E Tests   │  ← Full cluster, real GPUs
                    │    (10 tests)   │
                    └────────┬────────┘
                             │
                 ┌───────────┴───────────┐
                 │  Integration Tests    │  ← Kind cluster, mock GPUs
                 │     (100 tests)       │
                 └───────────┬───────────┘
                             │
         ┌───────────────────┴───────────────────┐
         │           Unit Tests                   │  ← No dependencies
         │          (1000 tests)                  │
         └───────────────────────────────────────┘
```

### Test Environments

| Environment | Purpose | GPU Support | Frequency |
|-------------|---------|-------------|-----------|
| Unit Tests | Fast feedback | Mock | Every commit |
| Integration (Kind) | API testing | Mock | Every PR |
| Integration (GPU) | Real GPU testing | NVIDIA | Nightly |
| E2E (GKE) | Cloud testing | A100 | Weekly |
| E2E (On-prem) | Real workloads | H100 | Pre-release |

### Validation Checklist per Phase

Each phase must pass before proceeding:

```
☐ All unit tests pass (>80% coverage)
☐ All integration tests pass
☐ E2E tests pass on real cluster
☐ Performance benchmarks met
☐ No memory leaks (72-hour stress test)
☐ Documentation updated
☐ Security review (if applicable)
☐ Code review by 2+ team members
```

---

## Risk Mitigation

### Technical Risks

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| NVIDIA driver changes break interception | Medium | High | Pin driver versions, extensive testing |
| NCCL internal changes | Medium | High | Version-specific handling, abstraction layer |
| Large GPU memory (80GB+) causes OOM | Medium | Medium | Streaming checkpoint, no full copy |
| CRIU incompatibility with GPU processes | Low | High | Custom CRIU patches, contribute upstream |
| Performance overhead too high | Low | Medium | Careful profiling, optional features |

### Operational Risks

| Risk | Mitigation |
|------|------------|
| Checkpoint storage fills up | TTL, quotas, alerting |
| Failed restore leaves orphan resources | Cleanup finalizers, garbage collection |
| Checkpoint during critical operation | Application health checks before checkpoint |

---

## Success Metrics

### Phase Completion Criteria

| Metric | Target |
|--------|--------|
| Checkpoint success rate | >99% |
| Checkpoint time (10GB GPU mem) | <10s |
| Checkpoint time (80GB GPU mem) | <60s |
| Restore time | <30s |
| Application downtime | <5s (live migration) |
| Memory overhead | <5% |
| CPU overhead | <1% |

### Production Readiness Criteria

- [ ] 10,000 successful checkpoint/restore cycles without data corruption
- [ ] 72-hour continuous operation without memory leaks
- [ ] Successful checkpoint of production vLLM workload
- [ ] Successful migration between nodes
- [ ] Security audit passed
- [ ] Performance benchmarks met
- [ ] Documentation complete
- [ ] Runbook for operations team

---

## Appendix: Key Technical Details

### A. CRIU Integration Details

```go
// CRIU command construction for container checkpoint
func buildCRIUCommand(pid int, opts *CheckpointOptions) []string {
    args := []string{
        "dump",
        "-t", strconv.Itoa(pid),
        "-D", opts.OutputDir,
        "--shell-job",           // Handle shell sessions
        "--tcp-established",     // Preserve TCP connections
        "--ext-unix-sk",         // Handle external unix sockets
        "--file-locks",          // Preserve file locks
        "--leave-running",       // Don't kill after checkpoint (if requested)
    }
    
    // GPU-specific: exclude GPU device file descriptors
    // We handle GPU state separately
    args = append(args, "--external", "dev:/dev/nvidia*")
    
    return args
}
```

### B. CUDA Interception Implementation

```c
// Simplified interception for cuMemAlloc
CUresult cuMemAlloc(CUdeviceptr *dptr, size_t bytesize) {
    // Call real cuMemAlloc
    CUresult result = real_cuMemAlloc(dptr, bytesize);
    
    if (result == CUDA_SUCCESS) {
        // Track allocation
        allocation_tracker_add(*dptr, bytesize);
        
        // Notify agent (async, non-blocking)
        notify_agent_allocation(*dptr, bytesize);
    }
    
    return result;
}
```

### C. Checkpoint File Format

```
checkpoint/
├── manifest.json           # Metadata, version, options
├── process/
│   ├── criu/              # CRIU checkpoint images
│   │   ├── core-1.img
│   │   ├── mm-1.img
│   │   ├── pages-1.img
│   │   └── ...
│   └── metadata.json      # Process metadata
├── gpu/
│   ├── device-0/
│   │   ├── allocations.json
│   │   ├── memory.bin.zst    # Compressed GPU memory
│   │   └── context.json
│   └── device-1/
│       └── ...
├── ipc/
│   ├── shm/               # Shared memory regions
│   └── sockets/           # Unix socket state
└── nccl/
    ├── communicators.json # NCCL communicator state
    └── topology.json      # NCCL topology info
```

---

*This document is a living document and will be updated as we progress through implementation.*

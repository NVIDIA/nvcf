# NVSNAP Quick Reference

## Project Summary

**NVSNAP** = GPU Checkpoint/Restore for Kubernetes

**Goal**: Transparently checkpoint and restore GPU workloads (CUDA → PyTorch → vLLM) without application modification.

---

## Key Documents

| Document | Purpose |
|----------|---------|
| [ARCHITECTURE.md](architecture/ARCHITECTURE.md) | Complete system architecture and implementation plan |
| [VLLM-DEEP-DIVE.md](architecture/VLLM-DEEP-DIVE.md) | Technical deep dive on vLLM challenges |
| [PHASE-0-FOUNDATION.md](milestones/PHASE-0-FOUNDATION.md) | Detailed Phase 0 implementation guide |

---

## Implementation Timeline

```
Phase 0: Foundation         [Weeks 1-4]    ← START HERE
Phase 1: Simple CUDA        [Weeks 5-10]   
Phase 2: PyTorch            [Weeks 11-18]  
Phase 3: Multi-Process      [Weeks 19-28]  
Phase 4: vLLM               [Weeks 29-40]  
Phase 5: Production         [Weeks 41-52]  
```

**Each phase ends with real-cluster validation before proceeding.**

---

## Core Design Decisions

### 1. No Application Modification
- All interception via `LD_PRELOAD`
- Transparent to application
- Works with unmodified binaries

### 2. Runtime Agnostic
- Works with containerd, CRI-O
- Uses CRI interface, not runtime-specific APIs
- Kubernetes version agnostic (1.24+)

### 3. GPU Strategy
- Quiesce GPU before checkpoint (cudaDeviceSynchronize)
- Dump GPU memory via cuMemcpy
- Reconstruct CUDA context on restore
- NCCL reinitialization (not checkpointed)

### 4. Multi-Process
- Checkpoint entire process tree
- Coordinated freeze via cgroup
- IPC state captured and restored
- NCCL communicators reinitialized

---

## Technology Stack

| Layer | Technology |
|-------|------------|
| Language | Go 1.22+, Rust 1.75+ (GPU), TypeScript (UI) |
| Framework | controller-runtime, React 18+ |
| Checkpoint | CRIU 3.18+ |
| Storage | S3, GCS, Azure Blob, NFS, local |
| Monitoring | Prometheus, OpenTelemetry |

---

## Component Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      NVSNAP COMPONENTS                        │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  CONTROL PLANE                                               │
│  ├── nvsnap-server        REST API + web UI + catalog       │
│  └── CRDs                 GPUCheckpoint state machine        │
│                                                              │
│  DATA PLANE                                                  │
│  ├── nvsnap-agent         DaemonSet on GPU nodes            │
│  ├── libnvsnap.so         CUDA interception (LD_PRELOAD)    │
│  └── libnvsnap-nccl.so    NCCL interception (LD_PRELOAD)    │
│                                                              │
│  USER INTERFACE                                              │
│  ├── nvsnapctl            CLI tool                           │
│  └── Web UI              React-based dashboard              │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## CRD Quick Reference

### Checkpoint
```yaml
apiVersion: nvsnap.io/v1alpha1
kind: Checkpoint
metadata:
  name: my-checkpoint
spec:
  target:
    kind: Pod
    name: my-gpu-pod
  storage:
    type: s3
    bucket: my-bucket
    path: /checkpoints
  options:
    compress: true
    leaveRunning: false
```

### Restore
```yaml
apiVersion: nvsnap.io/v1alpha1
kind: Restore
metadata:
  name: my-restore
spec:
  checkpoint: my-checkpoint
  target:
    namespace: default
```

---

## Development Quick Start

```bash
# Clone and setup
git clone <repository-url> nvsnap
cd nvsnap

# Install dependencies
make tools

# Build
make build

# Run tests
make test

# Create local cluster
./scripts/cluster-up.sh

# Deploy to local cluster
make deploy-dev
```

---

## Key Challenges & Solutions

| Challenge | Solution |
|-----------|----------|
| GPU state is opaque | Quiesce + memory dump + reconstruct |
| NCCL cannot be checkpointed | Reinitialize on restore |
| GPU addresses may differ | Pointer remapping |
| Multi-process consistency | Cgroup atomic freeze |
| Large GPU memory | Streaming + compression |
| vLLM complexity | Coordinated multi-rank protocol |

---

## Validation Checkpoints

### Phase 0 Complete When:
- [ ] All binaries build
- [ ] Tests pass (>80% coverage)
- [ ] Helm installs on kind
- [ ] CRDs work
- [ ] CI is green

### Phase 1 Complete When:
- [ ] Simple CUDA app checkpoints
- [ ] Restore succeeds
- [ ] Works on real GPU

### Phase 4 Complete When:
- [ ] vLLM TP=4 checkpoints
- [ ] NCCL reinitializes
- [ ] Responses identical
- [ ] Spot preemption works

---

## Resources

- **Docs**: the [`docs/`](.) directory and the top-level [README](../README.md)
- **Issues**: the project issue tracker

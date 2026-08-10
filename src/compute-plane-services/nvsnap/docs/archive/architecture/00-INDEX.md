# NVSNAP Architecture Documentation

## Quick Links

| Document | Description |
|----------|-------------|
| [01-OVERVIEW.md](01-OVERVIEW.md) | System architecture and design principles |
| [03-INTERCEPTION-LAYER.md](03-INTERCEPTION-LAYER.md) | CUDA/NCCL interception deep dive |
| [04-RUNTIME-AGNOSTIC.md](04-RUNTIME-AGNOSTIC.md) | Container runtime independence |
| [05-MULTI-PROCESS.md](05-MULTI-PROCESS.md) | vLLM and multi-process coordination |

## Executive Summary

NVSNAP is a GPU checkpoint/restore system for Kubernetes that enables:

- **Live migration** of GPU workloads between nodes
- **Spot instance resilience** with automatic checkpoint on preemption
- **Fast cold starts** via checkpoint-based warm pools
- **Resource optimization** through dynamic workload placement

### Key Differentiators

| Feature | NVSNAP | Traditional Approach |
|---------|-------|---------------------|
| Application modification | **None required** | Often requires SDK integration |
| Container runtime | **Agnostic** | Usually tied to containerd/CRI-O |
| vLLM support | **Full (tensor parallel, KV cache)** | Limited or none |
| Multi-process NCCL | **Coordinated checkpoint** | Usually not supported |

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              NVSNAP SYSTEM                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      CONTROL PLANE                                   │   │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌────────────┐  │   │
│  │  │ API Server  │  │ Controller  │  │Orchestrator │  │   Web UI   │  │   │
│  │  └─────────────┘  └─────────────┘  └─────────────┘  └────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                        │                                    │
│  ┌─────────────────────────────────────┼────────────────────────────────┐  │
│  │                      DATA PLANE     │                                │  │
│  │  ┌────────────────────────────────────────────────────────────────┐  │  │
│  │  │                      NODE AGENT                                 │  │  │
│  │  │  ┌────────────┐  ┌────────────┐  ┌────────────┐               │  │  │
│  │  │  │ Discovery  │  │ C/R Engine │  │  Storage   │               │  │  │
│  │  │  └────────────┘  └────────────┘  └────────────┘               │  │  │
│  │  └────────────────────────────────────────────────────────────────┘  │  │
│  │                              │                                        │  │
│  │  ┌────────────────────────────────────────────────────────────────┐  │  │
│  │  │        INTERCEPTION LAYER (libnvsnap.so via LD_PRELOAD)         │  │  │
│  │  │                                                                 │  │  │
│  │  │  • Tracks all CUDA allocations, contexts, streams              │  │  │
│  │  │  • Tracks NCCL communicators and operations                    │  │  │
│  │  │  • Enables transparent checkpoint/restore                       │  │  │
│  │  │  • Zero application modification                                │  │  │
│  │  └────────────────────────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Milestone Overview

| Phase | Duration | Goal | Validation |
|-------|----------|------|------------|
| **M1: Foundation** | 4 weeks | CRIU + runtime-agnostic process C/R | Simple processes on containerd & CRI-O |
| **M2: Simple CUDA** | 6 weeks | Single GPU checkpoint | CUDA samples, cuBLAS |
| **M3: PyTorch** | 6 weeks | Training loop C/R | ResNet50, BERT fine-tuning |
| **M4: Multi-GPU** | 8 weeks | NCCL + DDP + FSDP | Multi-GPU training |
| **M5: vLLM** | 10 weeks | Full inference support | LLaMA-70B tensor parallel |
| **M6: Production** | 10 weeks | HA, UI, multi-cluster | Production deployment |

**Total: ~44 weeks**

## Technology Stack

| Component | Technology | Rationale |
|-----------|------------|-----------|
| Agent & Controller | Go 1.22+ | K8s ecosystem, excellent concurrency |
| Interception Library | C/C++ | Low-level CUDA interception, minimal overhead |
| GPU Memory Operations | CUDA Driver API | Direct memory access without runtime |
| Process C/R | CRIU | Industry standard, well-tested |
| API | gRPC + REST | Type-safe, efficient |
| UI | React + TypeScript | Modern, responsive |
| Storage | S3/GCS/NFS | Pluggable backends |

## Key Technical Decisions

### 1. Library Interposition vs. Kernel Module

**Decision**: Library interposition (LD_PRELOAD)

**Why**:
- No kernel dependencies
- Works with any kernel version
- Easier to deploy (no privileged operations)
- Can be updated without node restart

### 2. Runtime Agnostic

**Decision**: Use Linux primitives, not container runtime APIs

**Why**:
- No dependency on containerd/CRI-O versions
- Single code path for all runtimes
- Future-proof for new runtimes
- More reliable (fewer external dependencies)

### 3. NCCL Reconstruction

**Decision**: Track creation parameters, recreate on restore

**Why**:
- NCCL communicators can't be serialized
- New connections are created on restore
- Application sees same handles (remapped internally)
- Works transparently for any NCCL-using application

### 4. vLLM Multi-Process

**Decision**: Coordinated checkpoint with barrier protocol

**Why**:
- All processes must checkpoint atomically
- NCCL operations must be drained first
- Shared memory and IPC must be consistent
- KV cache can be optimized (only allocated blocks)

## Getting Started (Development)

### Prerequisites

```bash
# System requirements
- Linux kernel 5.10+
- NVIDIA Driver 525+
- CUDA 11.8+ or 12.x
- Go 1.22+
- GCC/Clang for C library
- Kind or minikube for local testing
```

### Quick Start

```bash
# Clone repository
git clone https://github.com/nvsnap/nvsnap.git
cd nvsnap

# Install tools
make tools

# Build all components
make build

# Run tests
make test

# Start local cluster
make cluster-up

# Deploy in development mode
make deploy-dev
```

### Project Structure

```
nvsnap/
├── api/                    # Protobuf and OpenAPI definitions
├── cmd/
│   ├── nvsnap-agent/        # Node agent
│   ├── nvsnap-controller/   # Kubernetes controller
│   ├── nvsnap-api/          # API server
│   └── nvsnapctl/           # CLI tool
├── lib/
│   └── libnvsnap/           # Interception library (C)
├── internal/
│   ├── agent/              # Agent implementation
│   ├── checkpoint/         # Checkpoint engine
│   ├── coordinator/        # Multi-process coordination
│   ├── discovery/          # Workload discovery
│   └── storage/            # Storage backends
├── pkg/
│   ├── apis/               # Kubernetes API types
│   └── cri/                # CRI abstraction
├── ui/                     # Web UI
├── deploy/
│   └── helm/               # Helm charts
├── docs/
│   └── architecture/       # This documentation
└── test/                   # Tests
```

## Risk Assessment

| Risk | Impact | Likelihood | Mitigation |
|------|--------|------------|------------|
| NVIDIA API changes | High | Low | Abstract CUDA calls, version detection |
| CRIU limitations with GPU | High | Medium | Extensive testing, fallback strategies |
| NCCL version incompatibility | Medium | Medium | Version detection, compat layer |
| vLLM internal changes | Medium | High | Abstract vLLM detection, follow releases |
| Performance overhead | Medium | Low | Benchmarking, optimization phase |

## Success Criteria

### Milestone 1 Complete
- [ ] CRIU checkpoint/restore works
- [ ] Runtime-agnostic process discovery
- [ ] Works on containerd AND CRI-O
- [ ] 80%+ unit test coverage

### Milestone 2 Complete
- [ ] Single GPU CUDA programs checkpoint/restore
- [ ] libnvsnap.so with <5% overhead
- [ ] Works with CUDA 11.8 and 12.x

### Milestone 3 Complete
- [ ] PyTorch training checkpoints mid-epoch
- [ ] Training loss continues correctly after restore
- [ ] DataLoader state preserved

### Milestone 4 Complete
- [ ] Multi-GPU DDP training works
- [ ] NCCL communicators reconstructed
- [ ] FSDP sharding preserved

### Milestone 5 Complete
- [ ] vLLM inference checkpoint/restore
- [ ] KV cache preserved efficiently
- [ ] Tensor parallelism works

### Milestone 6 Complete
- [ ] Production-ready with HA
- [ ] Web UI functional
- [ ] Multi-cluster support
- [ ] 99.9% availability in soak test

## Next Steps

1. **Read the detailed architecture docs** in this folder
2. **Set up development environment** with `make tools`
3. **Start with Milestone 1** - foundation is critical
4. **Run tests after every change** with `make test`
5. **Validate on real cluster** before declaring milestone complete

## Questions?

Open an issue or reach out to the team for architecture discussions.

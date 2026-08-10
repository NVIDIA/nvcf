# GPU Checkpoint/Restore Approaches

## Overview

This document describes three approaches to GPU checkpoint/restore, their current status, and roadmap.

## Approach 1: Kubernetes Native (CRI Checkpoint)

**Status: 🔴 Roadmap**

Uses Kubernetes' built-in checkpoint API via containerd/CRI-O.

```bash
# Requires K8s 1.25+ with ContainerCheckpoint feature gate
kubectl checkpoint <pod> -n <namespace> -c <container>

# Or via crictl
crictl checkpoint <container-id> --export=/path/to/checkpoint.tar
```

### How it works
1. K8s API calls containerd checkpoint
2. containerd uses runc + CRIU
3. CRIU dumps process state
4. cuda-checkpoint handles GPU state (if integrated)

### Current Blockers
- NVIDIA container runtime conflicts with CRIU mount handling
- Requires custom NVIDIA-aware CRIU or runtime modifications
- Feature gate not enabled on most production clusters

### Roadmap
- Work with NVIDIA to improve nvidia-container-runtime checkpoint support
- Test with upcoming containerd releases

---

## Approach 2: CRIU on Containerized Processes

**Status: 🟡 Experimental**

Run CRIU directly on containerized processes from the host.

```bash
# From host, targeting container process
PID=$(pgrep -f my_gpu_app)
cuda-checkpoint --action lock --pid $PID
cuda-checkpoint --action checkpoint --pid $PID
criu dump -t $PID -D /checkpoint --skip-mnt-ns --external net[]
```

### How it works
1. Find container's main process PID on host
2. Use cuda-checkpoint for GPU state
3. Use forked CRIU with container-aware flags
4. Restore requires matching container environment

### Current Status
- ✅ Dump works with forked CRIU
- ❌ Restore fails due to namespace/filesystem mismatches
- Requires binary at same path in restore environment

### Roadmap
- Implement restore-into-container support
- Bundle required binaries in checkpoint archive

---

## Approach 3: Host-Based Process Checkpoint ✅

**Status: 🟢 Working**

Run GPU workloads directly on the host (not containerized) and use CRIU + cuda-checkpoint.

```bash
# Start workload on host
./my_gpu_app &
PID=$!

# Checkpoint
cuda-checkpoint --action lock --pid $PID --timeout 10000
cuda-checkpoint --action checkpoint --pid $PID
criu dump -t $PID -D /checkpoint --shell-job

# Restore
criu restore -D /checkpoint --shell-job --restore-detached
NEW_PID=$(pgrep -f my_gpu_app)
cuda-checkpoint --action restore --pid $NEW_PID
cuda-checkpoint --action unlock --pid $NEW_PID

# Process continues from where it left off!
```

### How it works
1. GPU process runs directly on host
2. cuda-checkpoint saves/restores GPU state (memory, contexts)
3. CRIU saves/restores CPU state (memory, registers, file descriptors)
4. Process resumes execution from checkpoint

### Requirements
- NVIDIA Driver 555+ (for cuda-checkpoint)
- Forked CRIU with NVIDIA fixes (v4.2.1+)
- Root/sudo access on the host
- Process binary available at same path

### Verified Working
- ✅ Simple CUDA memory allocation
- ✅ Multi-threaded GPU processes
- ✅ GPU memory integrity preserved
- ✅ Same PID after restore
- ✅ Same GPU pointer addresses

---

## Comparison

| Feature | K8s Native | CRIU Container | Host Process |
|---------|------------|----------------|--------------|
| Production Ready | ❌ | ❌ | ✅ |
| Container Support | ✅ | ⚠️ | ❌ |
| GPU State | ❓ | ✅ | ✅ |
| Cross-node Migration | ❓ | ⚠️ | ⚠️ |
| No Code Changes | ✅ | ✅ | ✅ |

---

## Demo (Approach 3)

See `/scripts/demo-checkpoint.sh` for a working demonstration.

```bash
# Run the demo
./scripts/demo-checkpoint.sh

# Or with UI
cd ui && npm run dev
```

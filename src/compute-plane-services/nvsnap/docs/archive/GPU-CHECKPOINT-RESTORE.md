# GPU Checkpoint/Restore - Working Solution

## Overview

This document describes the working GPU checkpoint/restore solution for ML workloads.

## Architecture

The solution uses two components:

1. **cuda-checkpoint** (NVIDIA's tool) - Handles GPU state checkpoint/restore
2. **CRIU** (with cuda plugin) - Handles CPU state, file descriptors, and memory

### Workflow

```
1. Application runs with GPU (vLLM, PyTorch, etc.)
     |
2. cuda-checkpoint --action lock --pid <PID>
     |
3. cuda-checkpoint --action checkpoint --pid <PID>
     |
4. CRIU dump (captures CPU state, memory, FDs)
     |
5. [Process killed, can be migrated]
     |
6. CRIU restore (restores CPU state)
     |
7. cuda-checkpoint --action restore --pid <PID>
     |
8. cuda-checkpoint --action unlock --pid <PID>
     |
9. Application resumes with GPU state intact
```

## Requirements

- NVIDIA driver 555+ (for cuda-checkpoint support)
- cuda-checkpoint binary (from NVIDIA)
- CRIU with CUDA plugin
- Root privileges for CRIU operations

## Tested Workloads

| Workload | Status | Notes |
|----------|--------|-------|
| Simple CUDA | ✅ PASS | Basic cudaMalloc/cudaMemcpy |
| PyTorch | ✅ PASS | Neural network inference |
| vLLM | ✅ PASS | With io_uring fixes in CRIU fork |

## Test Scripts

```bash
# Simple CUDA test
sudo ./lib/nvsnap_intercept/tests/test_full_checkpoint_restore.sh

# PyTorch test  
sudo ./lib/nvsnap_intercept/tests/test_full_checkpoint_restore.sh pytorch

# vLLM test
sudo ./lib/nvsnap_intercept/tests/test_vllm_checkpoint.sh
```

## CRIU Plugin Modifications

The CRIU cuda plugin (`criu/plugins/cuda/cuda_plugin.c`) was modified to:

1. **Dynamic NVIDIA major detection** - Reads `/proc/devices` at runtime to detect all NVIDIA device major numbers (nvidia, nvidia-uvm, nvidia-caps, nvidia-nvswitch, nvidia-nvlink, etc.)

2. **Multi-major support** - Uses an array to store all detected NVIDIA majors instead of single variables

3. **Device file mapping** - Saves NVIDIA device FD paths during dump and restores them during restore

4. **VMA handling** - Maps NVIDIA VMAs to `/dev/zero` during restore (cuda-checkpoint handles actual GPU memory)

## Known Limitations

1. **Multi-process coordination** - Complex multi-process applications require `--tcp-established` flag for TCP socket checkpointing

2. **GPU memory size** - Large models may require careful memory management during checkpoint

## io_uring Support

The CRIU fork at `$HOME/personal/criu-orig/criu` includes io_uring fixes:

| Commit | Fix |
|--------|-----|
| `9fcb7b8fc` | Exclude io_uring VMAs from `VMA_UNSUPP` check |
| `2a2581b1d` | Skip SQPOLL threads (recreated on restore) |
| `eb153ad03` | Fix FD handling, VMA inheritance, fallback mapping |

These fixes allow vLLM (which uses uvloop/io_uring) to checkpoint/restore correctly.

## Role of libnvsnap_intercept.so

**Note: The interception library is NOT required for checkpoint/restore.**

cuda-checkpoint handles GPU state at the driver level. The interception library
is optional and provides:

1. **API call tracking** - For debugging CUDA/NCCL usage
2. **State visibility** - Shows allocations, streams, events (debugging)
3. **Future use** - Cross-node migration may need pointer remapping

For production use, you don't need `LD_PRELOAD=libnvsnap_intercept.so`.

## Future Work

1. Fix io_uring compatibility for vLLM
2. Implement Kubernetes integration
3. Add checkpoint migration between nodes
4. Optimize checkpoint size with incremental checkpoints

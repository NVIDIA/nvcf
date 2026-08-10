# GPU Restore Architecture: NvSnap + cuda-checkpoint (Path A)

## Key Insight (validated 2026-03-16)

- `cudaMalloc` / `cudaFree` **WORK** in all 4 TP workers post-CRIU (tested on H100)
- `cuMemCreate` (VMM API) **FAILS** with error 304 post-CRIU
- `cuda-checkpoint --action restore` fails with "operation cannot be performed in present state" when lock/checkpoint were skipped during dump
- cuda-checkpoint lock/checkpoint/restore are a matched set — can't use restore without lock+checkpoint

## Architecture: NvSnap handles data, cuda-checkpoint handles RM state

### Checkpoint Flow

```
1. NvSnap agent freezes cgroup
2. NvSnap agent writes trigger files, unfreezes
3. libnvsnap_intercept (each process):
   a. Destroys NCCL comms (existing, no-op currently)
   b. Calls nvsnap_checkpoint_save(checkpoint_dir)  ← D2H copy, 72GB/GPU
   c. Writes done marker
4. NvSnap agent waits for all done markers (180s timeout)
5. NvSnap agent: cuda-checkpoint --action lock --pid <each TP worker>
6. NvSnap agent: cuda-checkpoint --action checkpoint --pid <each TP worker>
   NOTE: This saves RM metadata only — GPU memory contents already saved by NvSnap
7. NvSnap agent: CRIU dump (process memory, FDs, etc.)
8. NvSnap agent: cuda-checkpoint --action unlock --pid <each TP worker>
```

**Key change from current flow**: cuda-checkpoint lock/checkpoint run AFTER NvSnap save (step 5-6), not before. NCCL comms are already destroyed by step 3, which may prevent the spin-poll issue during lock. If lock still hangs, we need to investigate further.

**Question**: Does cuda-checkpoint lock work after NCCL comms are destroyed? Previous eBPF showed it still spin-polls. BUT — that was with the old NvSnap. The new NvSnap save calls `cudaDeviceSynchronize` which fully quiesces GPU operations. Need to re-test lock after NvSnap save.

### Restore Flow

```
1. CRIU restore (processes frozen, RM state from cuda-checkpoint)
2. PostRestore hook: cuda-checkpoint --action restore --pid <each TP worker>
   This fixes the RM state so CUDA APIs work (validated: cudaMalloc works post-CRIU)
3. PostRestore hook: write /run/nvsnap-restore-dir config file
4. Processes unfreeze
5. SIGUSR2 delivered (selective, to main threads only)
6. libnvsnap_intercept SIGUSR2 handler fires in each TP worker:
   a. Detects /run/criu-restored marker
   b. Calls nvsnap_perform_restore_reinit()
   c. Reinit calls nvsnap_checkpoint_restore_self(checkpoint_dir)
   d. NvSnap: reads meta.bin, cudaMalloc at original VAs, cudaMemcpy H2D
7. vLLM resumes with GPU memory restored
```

**Key change**: Step 2 is NEW — cuda-checkpoint restore runs in PostRestore before unfreeze. This restores the NVIDIA RM state so CUDA APIs (cudaMalloc, cudaMemcpy) work in step 6d.

### NvSnap restore_self Changes Needed

Currently uses VMM APIs (cuMemAddressReserve + cuMemCreate + cuMemMap) which fail post-CRIU with error 304.

**New approach**: Use standard cudaMalloc + cudaMemcpy:
1. For each saved allocation: `cudaMalloc(size)` — returns new VA (may differ from original)
2. `cudaMemcpy(new_ptr, host_data, size, H2D)` — copy data back
3. If VA differs from original: report VA mismatch (PyTorch pointers may be invalid)

**VA preservation concern**: cudaMalloc doesn't guarantee the same VA. But after CRIU restore, the GPU VA space should be clean (RM restored from checkpoint). The caching allocator's pool may get the same VA if allocated in the same order and size.

### Environment Variables

At checkpoint time (set in workload pod manifest):
- `CHECKPOINT_PATH=/checkpoints` — hostPath mount
- `CHECKPOINT_ID` — set by agent at runtime
- `NVSNAP_GPU_INTERPOSE=1` — enables NvSnap interposition mode
- `NVSNAP_QUIESCE_SIGNALS=1` — enables signal-based quiesce
- `NVSNAP_GPU_LOG_LEVEL=3` — NvSnap logging

At restore time (set in restore pod manifest):
- `CHECKPOINT_PATH=/checkpoints`
- `CHECKPOINT_ID=<from checkpoint>`
- Remove `NVSNAP_SKIP_CUDA_CHECKPOINT` — let cuda-checkpoint resume run
- `NVSNAP_SKIP_SIGUSR2=0` — enable selective SIGUSR2

### Implementation Tasks

#### NvSnap Changes (checkpoint.go)

1. After NvSnap save + done markers received (existing step):
   - Run `cuda-checkpoint --action lock --pid <pid>` for each GPU process
   - Run `cuda-checkpoint --action checkpoint --pid <pid>` for each GPU process
   - If lock hangs (>30s timeout), skip and proceed (best effort)
2. After CRIU dump:
   - Run `cuda-checkpoint --action unlock --pid <pid>` for each GPU process
3. Remove `NVSNAP_SKIP_CUDA_CHECKPOINT` from checkpoint env — or gate it behind `NVSNAP_GPU_INTERPOSE`

#### NvSnap Changes (restore-entrypoint PostRestore)

1. Run `cuda-checkpoint --action restore --pid <pid>` for each GPU process
   - The restore-entrypoint can exec cuda-checkpoint for each PID
   - PIDs are known from the CRIU restore notification
2. Remove the hardcoded `os.Setenv("NVSNAP_SKIP_CUDA_CHECKPOINT", "1")` for interposition mode
   - Or: set it only during dump, unset during restore

#### NvSnap Changes (restore_self)

1. Replace VMM restore (cuMemCreate fails with 304) with cudaMalloc + cudaMemcpy
2. Use `cudaGetDevice()` to determine correct device (validated: works post-CRIU)
3. Allocate at any VA, copy data, report if VA differs from original

### Risk: cuda-checkpoint lock may still hang

The spin-poll on RM ioctls happened even after NCCL destruction. If it happens after NvSnap save too, we need Plan B:

**Plan B**: Skip cuda-checkpoint lock/checkpoint entirely. Instead:
- Accept that VMM APIs don't work post-CRIU
- Use cudaMalloc (standard API, works) for restore
- Accept potential VA mismatch
- If PyTorch's caching allocator gets the same pool VA (likely if allocation order is preserved), inference works

### Test Evidence

| API | Post-CRIU Result | Tested |
|-----|-----------------|--------|
| cudaMalloc | **WORKS** (all 4 TP workers) | 2026-03-16 |
| cudaFree | **WORKS** | 2026-03-16 |
| cuMemCreate (VMM) | **FAILS** error 304 | 2026-03-15 |
| cuMemAddressReserve | Works (VA=0) | 2026-03-15 |
| cuda-checkpoint restore (without prior lock) | FAILS "present state" | 2026-03-16 |
| cuda-checkpoint lock (multi-GPU) | Spin-polls indefinitely | 2026-03-12 (eBPF) |

### Logs

- cudaMalloc works: `$HOME/vllm-70b-restore-v17-cuda-works.log`
- cuda-checkpoint restore fails: `$HOME/vllm-70b-restore-v16-cuda-resume.log`
- VMM restore fails: `$HOME/vllm-70b-restore-v15-diagnostics.log`
- Device mismatch: `$HOME/vllm-70b-restore-v14-device-override.log`
- eBPF trace (cuda-checkpoint lock): `scripts/nccl-ioctl-trace.bt`

## Test Results: cuda-checkpoint lock after NvSnap save (2026-03-16)

cuda-checkpoint lock succeeds for the FIRST 3 processes (including 1 TP worker) but **hangs on the 4th** (second TP worker). Locking multiple GPUs sequentially deadlocks in the NVIDIA RM — the first GPU's locked state blocks the RM from responding to lock requests on other GPUs.

This means Path A (cuda-checkpoint for RM state) doesn't work for multi-GPU with sequential locking.

### Options

1. **Parallel lock**: Send lock to all GPU processes simultaneously. May work if the RM can handle concurrent lock requests. But cuda-checkpoint is a CLI tool — hard to parallelize.

2. **Plan B (no cuda-checkpoint)**: Skip cuda-checkpoint entirely. Use `cudaMalloc` (validated: works post-CRIU without RM fixup). Accept that VMM doesn't work. NvSnap restore uses standard CUDA APIs. VA may not match original — need to test if PyTorch tolerates different VAs.

3. **NVIDIA support**: Ask NVIDIA for a multi-GPU lock API that handles all GPUs atomically.

### Recommended: Plan B

Since `cudaMalloc` works post-CRIU without any cuda-checkpoint:
- Checkpoint: NvSnap saves D2H data (existing, works)
- Checkpoint: CRIU dump with `NVSNAP_SKIP_CUDA_CHECKPOINT=1` (existing, works)
- Restore: CRIU restore (existing, works)
- Restore: NvSnap `restore_self` using `cudaMalloc` + `cudaMemcpy` instead of VMM
- Risk: VA mismatch. PyTorch's caching allocator may or may not tolerate different VAs.

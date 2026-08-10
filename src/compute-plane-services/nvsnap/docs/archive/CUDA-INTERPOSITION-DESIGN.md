# CUDA API Interposition for Multi-GPU Checkpoint/Restore

**Status**: Draft
**Date**: 2026-03-06
**Issue**: #25 (NCCL quiescence for multi-GPU)
**Author**: NvSnap team

## Problem Statement

`cuda-checkpoint` (NVIDIA's official tool for GPU process checkpoint/restore) deadlocks on multi-GPU NCCL workloads. When a process has active NCCL communicators, `cuda-checkpoint --action lock` hangs indefinitely because the CUDA driver kernel module has cross-GPU state (NCCL ring buffers, peer-to-peer mappings, NVLink/NVSwitch state) that it cannot safely quiesce when processes are frozen.

This is a confirmed upstream limitation:
- [cuda-checkpoint#30](https://github.com/NVIDIA/cuda-checkpoint/issues/30) — NCCL deadlock on multi-GPU
- [cuda-checkpoint#45](https://github.com/NVIDIA/cuda-checkpoint/issues/45) — Hangs with NVLink active
- [cuda-checkpoint#27](https://github.com/NVIDIA/cuda-checkpoint/issues/27) — Multi-process CUDA IPC issues

**Current state**: All 5 single-GPU workloads (vLLM, SGLang, TRT-LLM) checkpoint/restore successfully using `cuda-checkpoint`. Multi-GPU (e.g. Llama-3.1-70B with TP=4 on 4xH100) is blocked entirely.

NvSnap already has NCCL quiesce support (`nccl_intercept.c`) that destroys NCCL communicators before checkpoint and recreates them after restore. But `cuda-checkpoint` still deadlocks even after NCCL comms are destroyed, because NCCL leaves residual driver-level state (proxy threads, CUDA IPC handles, peer memory mappings).

## Approach

Bypass `cuda-checkpoint` entirely by intercepting CUDA runtime API calls at the application level. The interception library (`libnvsnap_intercept.so`, loaded via `LD_PRELOAD`) already hooks io_uring, libuv, ZMQ, and NCCL functions. We extend it to intercept CUDA memory allocation calls, enabling us to:

1. **Track** all GPU memory allocations (device pointer, size, device ID, flags)
2. **Save** GPU memory contents to host before checkpoint (cudaMemcpy D2H)
3. **Release** all GPU resources so CRIU sees a GPU-free process
4. **Restore** GPU memory at the original virtual addresses after CRIU restore
5. **Reconstruct** NCCL communicators (already implemented)

This gives us full control over the GPU lifecycle without depending on `cuda-checkpoint` or the CUDA driver's internal checkpoint support.

## CUDA Functions to Intercept

### Memory Allocation

| Function | Signature | What We Track |
|----------|-----------|---------------|
| `cudaMalloc` | `cudaError_t cudaMalloc(void **devPtr, size_t size)` | dev_ptr, size, device_id |
| `cudaFree` | `cudaError_t cudaFree(void *devPtr)` | Remove from tracking |
| `cudaMallocAsync` | `cudaError_t cudaMallocAsync(void **devPtr, size_t size, cudaStream_t stream)` | dev_ptr, size, device_id, stream |
| `cudaFreeAsync` | `cudaError_t cudaFreeAsync(void *devPtr, cudaStream_t stream)` | Remove from tracking |
| `cudaMallocManaged` | `cudaError_t cudaMallocManaged(void **devPtr, size_t size, unsigned int flags)` | dev_ptr, size, flags (unified memory) |
| `cudaMallocHost` | `cudaError_t cudaMallocHost(void **ptr, size_t size)` | host_ptr, size (pinned memory) |
| `cudaFreeHost` | `cudaError_t cudaFreeHost(void *ptr)` | Remove from tracking |
| `cudaHostAlloc` | `cudaError_t cudaHostAlloc(void **pHost, size_t size, unsigned int flags)` | host_ptr, size, flags |

### IPC Memory

| Function | Signature | What We Track |
|----------|-----------|---------------|
| `cudaIpcGetMemHandle` | `cudaError_t cudaIpcGetMemHandle(cudaIpcMemHandle_t *handle, void *devPtr)` | Mark allocation as IPC-exported |
| `cudaIpcOpenMemHandle` | `cudaError_t cudaIpcOpenMemHandle(void **devPtr, cudaIpcMemHandle_t handle, unsigned int flags)` | dev_ptr, handle, flags (IPC-imported, do NOT free) |
| `cudaIpcCloseMemHandle` | `cudaError_t cudaIpcCloseMemHandle(void *devPtr)` | Remove IPC import tracking |

### Driver API (may also be needed)

PyTorch and some frameworks call the CUDA driver API directly for memory management:

| Function | Signature | What We Track |
|----------|-----------|---------------|
| `cuMemAlloc` | `CUresult cuMemAlloc(CUdeviceptr *dptr, size_t bytesize)` | dev_ptr, size |
| `cuMemAllocManaged` | `CUresult cuMemAllocManaged(CUdeviceptr *dptr, size_t bytesize, unsigned int flags)` | dev_ptr, size, flags |
| `cuMemFree` | `CUresult cuMemFree(CUdeviceptr dptr)` | Remove from tracking |

**Note**: PyTorch's `CUDACachingAllocator` calls `cudaMalloc` for large blocks and sub-allocates within them. We only need to track the outer `cudaMalloc` calls. The sub-allocation metadata lives in host memory and is preserved by CRIU.

## Data Structures

```c
typedef enum {
    NVSNAP_ALLOC_DEVICE = 0,      /* cudaMalloc */
    NVSNAP_ALLOC_DEVICE_ASYNC,    /* cudaMallocAsync */
    NVSNAP_ALLOC_MANAGED,         /* cudaMallocManaged */
    NVSNAP_ALLOC_HOST_PINNED,     /* cudaMallocHost / cudaHostAlloc */
    NVSNAP_ALLOC_IPC_IMPORT,      /* cudaIpcOpenMemHandle (don't free, don't save) */
} nvsnap_alloc_type_t;

typedef struct nvsnap_gpu_alloc {
    void           *dev_ptr;     /* GPU virtual address */
    size_t          size;        /* Allocation size in bytes */
    int             device_id;   /* CUDA device ordinal */
    nvsnap_alloc_type_t type;      /* Allocation type */
    unsigned int    flags;       /* Original allocation flags */
    void           *host_backup; /* Host buffer for D2H save (NULL until checkpoint) */
} nvsnap_gpu_alloc_t;

#define NVSNAP_GPU_MAX_ALLOCS 65536

static nvsnap_gpu_alloc_t g_gpu_allocs[NVSNAP_GPU_MAX_ALLOCS];
static int g_gpu_alloc_count = 0;
static pthread_mutex_t g_gpu_mutex = PTHREAD_MUTEX_INITIALIZER;
static size_t g_gpu_total_bytes = 0;
```

For production, the fixed-size array should be replaced with a hash map (dev_ptr -> alloc_info) for O(1) lookup on `cudaFree`. But for initial implementation, a linear scan over ~1000 allocations (typical for a vLLM process) is fast enough.

## Checkpoint Flow

```
Agent                              Worker Process (per GPU)
  |                                    |
  | 1. Create quiesce trigger          | quiesce thread polling
  |    /dev/shm/nvsnap-quiesce           |
  |                                    |
  |                                2. Worker sees trigger
  |                                3. cudaDeviceSynchronize() — drain GPU ops
  |                                4. Barrier: wait for all ranks
  |                                5. ncclCommFinalize + ncclCommDestroy
  |                                6. === NEW: GPU memory save ===
  |                                   For each tracked allocation:
  |                                     a. host_backup = malloc(size)
  |                                     b. cudaMemcpy(host_backup, dev_ptr, size, D2H)
  |                                     c. cudaFree(dev_ptr)
  |                                7. cudaDeviceReset() — release all GPU state
  |                                8. Write done marker
  |                                    |
  | 9. All done markers present        |
  | 10. CRIU dump (process has no GPU) |
  |                                    |
```

### Detailed checkpoint sequence (per process):

```c
void nvsnap_gpu_checkpoint(void) {
    // 1. Synchronize all GPU work
    cudaDeviceSynchronize();

    // 2. NCCL communicators already destroyed by nccl_intercept.c

    // 3. Save and release GPU memory
    pthread_mutex_lock(&g_gpu_mutex);
    for (int i = 0; i < g_gpu_alloc_count; i++) {
        nvsnap_gpu_alloc_t *a = &g_gpu_allocs[i];

        // Skip IPC imports — owned by another process
        if (a->type == NVSNAP_ALLOC_IPC_IMPORT) {
            cudaIpcCloseMemHandle(a->dev_ptr);
            continue;
        }

        // Skip host-pinned memory — already in host RAM, CRIU preserves it
        if (a->type == NVSNAP_ALLOC_HOST_PINNED)
            continue;

        // Allocate host backup buffer
        a->host_backup = malloc(a->size);
        if (!a->host_backup) {
            NVSNAP_ERROR("malloc(%zu) failed for GPU backup", a->size);
            // Fatal — cannot checkpoint without saving GPU state
            abort();
        }

        // Copy GPU -> Host
        cudaError_t err = cudaMemcpy(a->host_backup, a->dev_ptr,
                                      a->size, cudaMemcpyDeviceToHost);
        if (err != cudaSuccess) {
            NVSNAP_ERROR("cudaMemcpy D2H failed for ptr=%p size=%zu: %d",
                       a->dev_ptr, a->size, err);
            abort();
        }

        // Free GPU allocation
        cudaFree(a->dev_ptr);
    }
    pthread_mutex_unlock(&g_gpu_mutex);

    // 4. Release all GPU context state
    cudaDeviceReset();

    NVSNAP_INFO("GPU checkpoint: saved %d allocations, %zu bytes to host",
              g_gpu_alloc_count, g_gpu_total_bytes);
}
```

**Memory requirement**: Host RAM must have enough free memory to hold the entire GPU working set. For Llama-3.1-70B on 4xH100 (80GB each), that is up to 320GB of host RAM. This is typically available on DGX-class systems (1-2TB RAM).

## Restore Flow

```
CRIU restores process (no GPU state)
  |
  | Process resumes execution
  |
  | restore-entrypoint detects restore
  |    |
  |    | 1. === NEW: GPU memory restore ===
  |    |    For each tracked allocation:
  |    |      a. cuMemAddressReserve(dev_ptr, size) — reserve original VA
  |    |      b. cuMemCreate(&handle, size) — allocate physical memory
  |    |      c. cuMemMap(dev_ptr, size, handle) — map at original VA
  |    |      d. cuMemSetAccess(dev_ptr, size, ...) — set access permissions
  |    |      e. cudaMemcpy(dev_ptr, host_backup, size, H2D) — restore data
  |    |      f. free(host_backup)
  |    |
  |    | 2. Re-init NCCL communicators (already implemented)
  |    |
  |    | 3. Resume application
```

### Detailed restore sequence (per process):

```c
void nvsnap_gpu_restore(void) {
    // 1. Initialize CUDA (fresh context)
    cudaSetDevice(g_my_device_id);

    // 2. Restore each allocation at its original virtual address
    pthread_mutex_lock(&g_gpu_mutex);
    for (int i = 0; i < g_gpu_alloc_count; i++) {
        nvsnap_gpu_alloc_t *a = &g_gpu_allocs[i];

        if (a->type == NVSNAP_ALLOC_HOST_PINNED || a->type == NVSNAP_ALLOC_IPC_IMPORT)
            continue;

        if (!a->host_backup)
            continue;

        // Reserve the exact virtual address range
        CUdeviceptr reserved;
        CUresult res = cuMemAddressReserve(&reserved, a->size, 0,
                                            (CUdeviceptr)a->dev_ptr, 0);
        if (res != CUDA_SUCCESS || reserved != (CUdeviceptr)a->dev_ptr) {
            NVSNAP_ERROR("cuMemAddressReserve failed at %p: res=%d got=%p",
                       a->dev_ptr, res, (void*)reserved);
            // Fallback: try cudaMalloc and hope for the same address (unlikely)
            // This is a fatal error in practice
            abort();
        }

        // Create physical memory backing
        CUmemGenericAllocationHandle mem_handle;
        CUmemAllocationProp prop = {0};
        prop.type = CU_MEM_ALLOCATION_TYPE_PINNED;
        prop.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
        prop.location.id = a->device_id;

        res = cuMemCreate(&mem_handle, a->size, &prop, 0);
        if (res != CUDA_SUCCESS) {
            NVSNAP_ERROR("cuMemCreate failed: %d", res);
            abort();
        }

        // Map physical memory at the reserved virtual address
        res = cuMemMap((CUdeviceptr)a->dev_ptr, a->size, 0, mem_handle, 0);
        if (res != CUDA_SUCCESS) {
            NVSNAP_ERROR("cuMemMap failed at %p: %d", a->dev_ptr, res);
            abort();
        }

        // Set access permissions
        CUmemAccessDesc access = {0};
        access.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
        access.location.id = a->device_id;
        access.flags = CU_MEM_ACCESS_FLAGS_PROT_READWRITE;

        res = cuMemSetAccess((CUdeviceptr)a->dev_ptr, a->size, &access, 1);
        if (res != CUDA_SUCCESS) {
            NVSNAP_ERROR("cuMemSetAccess failed at %p: %d", a->dev_ptr, res);
            abort();
        }

        // Copy data back: Host -> GPU
        cudaError_t err = cudaMemcpy(a->dev_ptr, a->host_backup,
                                      a->size, cudaMemcpyHostToDevice);
        if (err != cudaSuccess) {
            NVSNAP_ERROR("cudaMemcpy H2D failed for ptr=%p: %d", a->dev_ptr, err);
            abort();
        }

        // Free host backup
        free(a->host_backup);
        a->host_backup = NULL;
    }
    pthread_mutex_unlock(&g_gpu_mutex);

    // 3. NCCL restore handled by nccl_intercept.c

    NVSNAP_INFO("GPU restore: restored %d allocations, %zu bytes to device",
              g_gpu_alloc_count, g_gpu_total_bytes);
}
```

## GPU Virtual Address Preservation

This is the single most critical aspect of the design, and the area with the most uncertainty.

### Why it matters

PyTorch tensors store `data_ptr()` as raw GPU virtual addresses (e.g. `0x7f1234000000`). These pointers are embedded throughout:
- The tensor objects themselves (host memory, preserved by CRIU)
- PyTorch's CUDACachingAllocator block metadata (host memory, preserved by CRIU)
- cuDNN workspace pointers
- NCCL send/receive buffer pointers
- vLLM's PagedAttention block tables

If GPU memory is restored at different virtual addresses, every one of these pointers becomes dangling. The application would crash immediately.

### The cuMemAddressReserve + cuMemMap approach

CUDA 10.2+ provides the Virtual Memory Management (VMM) API:

1. **`cuMemAddressReserve(ptr, size, alignment, addr, flags)`** — Reserve a virtual address range. The `addr` parameter requests a specific address. If the address is available, CUDA returns it. If not, the call fails with `CUDA_ERROR_OUT_OF_MEMORY`.

2. **`cuMemCreate(handle, size, prop, flags)`** — Allocate physical GPU memory without mapping it to any virtual address.

3. **`cuMemMap(ptr, size, offset, handle, flags)`** — Map physical memory to a virtual address range previously reserved with `cuMemAddressReserve`.

4. **`cuMemSetAccess(ptr, size, desc, count)`** — Set access permissions on the mapped range.

This 4-step sequence lets us place GPU memory at an exact virtual address, which is what we need.

### Why this might not work

The fundamental concern: addresses originally allocated by `cudaMalloc` come from the CUDA driver's internal virtual address allocator. After `cudaDeviceReset()`, the driver reinitializes and its VA allocator starts fresh. When we call `cuMemAddressReserve` requesting the old addresses, two things could go wrong:

1. **VA conflict**: The CUDA runtime itself allocates internal GPU memory during `cudaSetDevice()` / context creation. These internal allocations might land at addresses that overlap with our saved allocations. We would have no way to move them.

2. **Alignment mismatch**: `cudaMalloc` may use different alignment than `cuMemAddressReserve`. The VMM API requires `size` and `alignment` to be multiples of the allocation granularity (typically 2MB on H100). `cudaMalloc` may have allocated at finer granularity.

3. **Driver version dependence**: The CUDA driver's VA space layout may differ between driver versions, or even between reboots. An address that was valid in one session might not be reservable in another.

We won't know if this works until we try it. The first experiment should be:
- `cudaMalloc` a few buffers, note addresses
- `cudaFree` them all + `cudaDeviceReset()`
- Try `cuMemAddressReserve` at the same addresses
- Check if they succeed

### Fallback: address remapping

If `cuMemAddressReserve` at the original address fails, we are stuck. There is no generic way to "rewrite" all GPU pointers in a running process — they are scattered across host memory in arbitrary data structures. This is why `cuda-checkpoint` operates at the driver level: it can preserve the entire VA space without the application ever knowing.

One possible (ugly) mitigation: allocate all GPU memory using the VMM API from the start (intercept `cudaMalloc` and implement it via `cuMemAddressReserve` + `cuMemCreate` + `cuMemMap`). This way we always control the VA space. But this changes the allocation behavior and could break applications that depend on `cudaMalloc` semantics (e.g. alignment, stream-ordered allocation).

## CUDA State Beyond Memory

GPU memory is only part of the story. After `cudaDeviceReset()`, all CUDA state is destroyed:

| State | Preserved by our approach? | Notes |
|-------|---------------------------|-------|
| Device memory contents | Yes | D2H copy + H2D restore |
| Memory virtual addresses | Maybe | See cuMemAddressReserve discussion |
| CUDA contexts | No | Recreated implicitly by `cudaSetDevice()` |
| CUDA streams | No | Application holds stale stream handles |
| CUDA events | No | Application holds stale event handles |
| Loaded modules / kernels | No | cuModuleLoad state lost |
| cuDNN handles | No | Application holds stale handles |
| cuBLAS handles | No | Application holds stale handles |
| NCCL communicators | Yes | Already handled by `nccl_intercept.c` |
| CUDA IPC handles | No | Must be re-exported/imported |

The "No" entries are the problem. When the application resumes after restore, it will call CUDA APIs with stale handles (streams, events, cuDNN/cuBLAS handles). These calls will return errors or exhibit undefined behavior.

### How PyTorch uses CUDA state

PyTorch's typical usage:
- **Streams**: Created at startup, stored in `CUDAStreamPool`. Used for all compute. Stale streams would cause kernel launch failures.
- **cuBLAS/cuDNN handles**: Created per-device, cached globally. Used on every forward pass. Stale handles → segfault or CUDA error on first use.
- **Events**: Used for synchronization (e.g. `record()` / `wait()`). Less critical — many are short-lived.
- **Modules**: PTX/CUBIN loaded at first kernel launch, cached by driver. Would need to be reloaded (this happens automatically if the driver context is fresh).

### Possible mitigations

1. **Intercept stream/event/handle creation too**: Track all CUDA objects, recreate them at restore, build a remap table (like we do for NCCL comms). This is the full interposition approach — intercept dozens of CUDA APIs.

2. **Avoid cudaDeviceReset**: Instead of resetting the entire context, just free all memory. Streams, events, and handles survive. But then the CUDA driver still has the device open, and CRIU will try to checkpoint its device file descriptors, which was the original problem.

3. **Close device FDs manually**: Instead of `cudaDeviceReset()`, free all memory, then close the `/dev/nvidia*` file descriptors directly. Extremely dangerous — the CUDA runtime doesn't expect its FDs to disappear. Likely crashes.

None of these is clean. Option 1 (full interposition) is the only viable path, but it is a massive undertaking.

## Risks and Open Questions

### High Risk

1. **cuMemAddressReserve may not support our addresses**: If CUDA runtime internal allocations conflict with saved addresses, restore fails with no workaround. Must prototype before committing to this approach.

2. **CUDA context state cannot be partially preserved**: We either keep the full CUDA context (which prevents CRIU from checkpointing due to device FDs) or lose everything (streams, events, handles). There is no middle ground.

3. **Host RAM requirements**: Saving 4x80GB = 320GB of GPU memory requires 320GB of free host RAM. DGX H100 has 2TB, so this is feasible. Smaller systems (e.g. 512GB RAM, 8x80GB GPU) would not have enough.

### Medium Risk

4. **PyTorch CUDACachingAllocator sub-allocation**: We track outer `cudaMalloc` blocks. PyTorch sub-allocates within them. If we restore the outer block at the same address, sub-allocation metadata (in host memory, preserved by CRIU) should remain valid. But if PyTorch's allocator also stores pointers to CUDA allocator internal structures, those would be stale.

5. **cudaMallocAsync (stream-ordered pools)**: CUDA 11.2+ stream-ordered allocation uses memory pools. The pool state is internal to the driver. After `cudaDeviceReset()`, pools are gone. If the application uses `cudaMallocAsync`, we would need to intercept pool creation (`cudaMemPoolCreate`) and restore pool state.

6. **Unified memory (cudaMallocManaged)**: Lives in both host and device address spaces. The GPU VA must match the host VA (they may be the same in unified addressing). Saving/restoring requires special handling.

### Low Risk

7. **Performance**: D2H + H2D at PCIe Gen5 x16 bandwidth (~64 GB/s bidirectional). For 80GB: ~1.25s each direction. For 320GB across 4 GPUs (parallel): ~5s total. Acceptable for checkpoint/restore SLA.

8. **NCCL reconstruction**: Already implemented and tested for single-GPU. Multi-GPU NCCL reconstruction (with cross-rank barrier) needs testing but is a solved design.

### Open Questions

- Can `cuMemAddressReserve` reserve addresses that were previously allocated by `cudaMalloc`? Nobody has documented this use case.
- Does `cudaDeviceReset()` actually release all driver-level state (NVLink peer mappings, etc.) that would prevent CRIU from dumping?
- What does Cedana's `--gpu-freeze-type nccl` actually do? If they have solved this, their approach is not public.
- Can we avoid `cudaDeviceReset()` and instead selectively close NVIDIA device FDs after freeing all resources?

## Comparison with cuda-checkpoint

| Aspect | cuda-checkpoint | CUDA API Interposition |
|--------|----------------|----------------------|
| **Operates at** | CUDA driver (kernel module) | CUDA runtime (user space) |
| **Memory preservation** | Full — driver preserves VA space | Manual — must reserve+map at same VA |
| **Context preservation** | Full — streams, events, handles all preserved | None — everything except memory is lost |
| **NCCL support** | Deadlocks (confirmed bug) | Works — we destroy NCCL before checkpoint |
| **Application transparency** | Fully transparent | Transparent for memory; streams/events/handles break |
| **Host RAM overhead** | Zero (driver holds GPU state on-device) | Full copy of GPU memory to host RAM |
| **Checkpoint latency** | ~1s (driver internal snapshot) | ~5-10s (D2H copies) |
| **Implementation effort** | Already done (but broken for multi-GPU) | Large — weeks to months |
| **NVIDIA dependency** | Hard dependency on driver version | No dependency on cuda-checkpoint |
| **Risk** | Blocked by NVIDIA fixing bugs | Blocked by VA reservation uncertainty |

**Bottom line**: `cuda-checkpoint` is the right solution. It preserves all state transparently. The only reason we are considering interposition is because `cuda-checkpoint` does not work for multi-GPU NCCL workloads, and NVIDIA has not provided a timeline for a fix.

## Comparison with Cedana

[Cedana](https://cedana.ai) is a 2-person startup that has built GPU checkpoint/restore with `--gpu-freeze-type nccl` support for multi-GPU workloads. Their approach:

- Uses CRIU with a custom GPU plugin
- Intercepts CUDA calls via LD_PRELOAD (similar to NvSnap)
- Claims to support NCCL workloads on multi-GPU

Their implementation details are not public. Key unknowns:
- How they handle GPU VA preservation
- Whether they do full memory save/restore or something else
- Whether they bypass cuda-checkpoint or use a modified version
- What CUDA state they reconstruct (just memory? streams? handles?)

If Cedana has solved VA preservation and CUDA state reconstruction, it proves the approach is feasible. But we cannot verify this without access to their code.

## Effort Estimate

| Work Item | Complexity | Effort | Notes |
|-----------|-----------|--------|-------|
| **Prototype VA reservation** | Medium | 2-3 days | cudaMalloc → cudaFree → cuMemAddressReserve round-trip test |
| **CUDA malloc/free interception** | Low | 2-3 days | Similar pattern to existing NCCL interception |
| **Allocation tracking table** | Low | 1-2 days | Hash map or array, thread-safe |
| **D2H save (checkpoint)** | Medium | 3-5 days | Per-allocation cudaMemcpy, error handling, IPC handling |
| **H2D restore with VA preservation** | High | 5-10 days | VMM API, error handling, fallback strategies |
| **Stream/event remap** | High | 5-10 days | Track all create/destroy, rebuild remap table |
| **cuDNN/cuBLAS handle remap** | Medium | 3-5 days | Similar to stream remap |
| **Integration with quiesce flow** | Medium | 3-5 days | Wire into existing quiesce trigger + barrier |
| **Multi-GPU testing** | High | 5-10 days | TP=4 vLLM 70B, edge cases |
| **Total** | | **4-8 weeks** | Assuming VA reservation works |

If VA reservation does not work, the project is not feasible with this approach.

## Alternative: Sequential Per-Process cuda-checkpoint (Option 3)

**This should be tried first.** It is far simpler.

The hypothesis: `cuda-checkpoint` deadlocks because it tries to lock GPU processes that have active NCCL cross-GPU communication. If we:

1. Destroy all NCCL communicators (already implemented)
2. Close all CUDA IPC handles
3. `cudaDeviceSynchronize()` on all ranks
4. Run `cuda-checkpoint --action lock` on each process **one at a time** (not simultaneously)

...then `cuda-checkpoint` might succeed because there is no cross-GPU state remaining.

### Why this might work

After NCCL destroy + IPC close + device sync, each process should be an independent single-GPU process. `cuda-checkpoint` handles single-GPU processes fine (proven by our 5 passing workloads).

### Why this might not work

- NCCL's `ncclCommDestroy` may not fully clean up driver-level peer mappings
- The CUDA driver may retain NVLink/NVSwitch routing state even after communicators are destroyed
- `cuda-checkpoint` may have internal assumptions about process independence that our post-cleanup state violates

### Effort

| Work Item | Effort |
|-----------|--------|
| IPC handle close before checkpoint | 1-2 days |
| Sequential cuda-checkpoint in agent | 1-2 days |
| Testing with TP=4 vLLM 70B | 2-3 days |
| **Total** | **1-1.5 weeks** |

If this works, we get multi-GPU support with minimal code changes and zero risk of VA reservation failures. If it doesn't work, we fall back to the full interposition approach described in this document.

## Recommendation

1. **Try Option 3 first** (sequential cuda-checkpoint after NCCL quiesce). Effort: 1-1.5 weeks. If it works, we are done.

2. **If Option 3 fails**, prototype VA reservation to determine feasibility. Effort: 2-3 days for the experiment.

3. **If VA reservation works**, proceed with full CUDA interposition. Effort: 4-8 weeks.

4. **If VA reservation fails**, evaluate whether partial interposition (memory only, no streams/events) is good enough for specific workloads like vLLM where we understand the CUDA usage patterns. This would be a fragile, workload-specific solution.

5. **Watch cuda-checkpoint**: If NVIDIA fixes the multi-GPU NCCL deadlock upstream, all of this becomes unnecessary. Track issues #30, #45, #27 for updates.

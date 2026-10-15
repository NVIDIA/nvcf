/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Minimal CUDA Runtime API interception for checkpoint/restore.
 *
 * Only the functions needed for allocation tracking, device management,
 * and checkpoint quiesce are intercepted here. All pass-through wrappers
 * have been removed to avoid stack corruption from vendored type mismatches.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "nvsnap/gpu/cuda_types.h"
#include "nvsnap/gpu/interpose.h"
#include "nvsnap/gpu/tracker.h"
#include "nvsnap/gpu/metrics.h"
#include "nvsnap/gpu/checkpoint.h"

/* ═══════════════════════════════════════════════════════════════════════
 * Reliable device query
 *
 * nvsnap_current_device (thread-local) can be wrong if cudaSetDevice
 * was called via a path we don't intercept (driver API, before our
 * library loaded, from a different thread). Query the CUDA runtime
 * for the actual current device.
 * ═══════════════════════════════════════════════════════════════════════ */
int nvsnap_get_current_device(void)
{
    static cudaError_t (*real_get)(int *) = NULL;
    if (!real_get) {
        real_get = (cudaError_t (*)(int *))dlsym(RTLD_NEXT, "cudaGetDevice");
        if (!real_get)
            real_get = (cudaError_t (*)(int *))
                nvsnap_resolve_real("cudaGetDevice", "libcudart.so");
    }
    if (real_get) {
        int dev = -1;
        if (real_get(&dev) == 0 && dev >= 0) {
            nvsnap_current_device = dev; /* keep thread-local in sync */
            return dev;
        }
    }
    return nvsnap_current_device; /* fallback */
}

/*
 * No auto-detection of post-CRIU restore. NvSnap's libnvsnap_intercept.so
 * calls nvsnap_checkpoint_restore_self() directly from its reinit
 * handler when it detects the CRIU restore marker.
 *
 * This avoids:
 *   - Signal handler conflicts (SIGUSR2)
 *   - Marker file access() syscalls during checkpoint save
 *   - In-memory state that CRIU preserves incorrectly
 *   - Race conditions between save and deferred restore
 *
 * NvSnap's job: expose nvsnap_checkpoint_restore_self().
 * NvSnap's job: call it at the right time.
 */

/* Kept for ABI compatibility — no-op. */
void nvsnap_reset_restore_state(void) { }
void nvsnap_deferred_restore(void) { }

/* ═══════════════════════════════════════════════════════════════════════
 * Memory Allocation — tracked for checkpoint/restore
 * ═══════════════════════════════════════════════════════════════════════ */

/* ─── cudaMalloc ─────────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaMalloc, void **, size_t);

cudaError_t cudaMalloc(void **devPtr, size_t size)
{
    NVSNAP_LOAD_REAL(cudaMalloc);
    nvsnap_deferred_restore();

    cudaError_t err = real_cudaMalloc(devPtr, size);

    if (err == cudaSuccess && devPtr && *devPtr) {
        nvsnap_tracker_track_alloc((uintptr_t)*devPtr, size,
                                     nvsnap_get_current_device(), 0 /* DEVICE */);
        nvsnap_tracker_add_allocated_bytes((int64_t)size);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), size,
                               (uint64_t)(uintptr_t)*devPtr);
        NVSNAP_GPU_LOG_DEBUG("cudaMalloc(%zu) -> %p", size, *devPtr);
    }
    return err;
}

/* ─── cudaFree ───────────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaFree, void *);

cudaError_t cudaFree(void *devPtr)
{
    NVSNAP_LOAD_REAL(cudaFree);

    if (devPtr) {
        nvsnap_tracker_untrack_alloc((uintptr_t)devPtr);
        nvsnap_metrics_write(NVSNAP_METRIC_FREE,
                               nvsnap_get_current_device(), 0,
                               (uint64_t)(uintptr_t)devPtr);
        NVSNAP_GPU_LOG_DEBUG("cudaFree(%p)", devPtr);
    }

    return real_cudaFree(devPtr);
}

/* ─── cudaMallocManaged ──────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaMallocManaged, void **, size_t, unsigned);

cudaError_t cudaMallocManaged(void **devPtr, size_t size, unsigned flags)
{
    NVSNAP_LOAD_REAL(cudaMallocManaged);
    nvsnap_deferred_restore();

    cudaError_t err = real_cudaMallocManaged(devPtr, size, flags);

    if (err == cudaSuccess && devPtr && *devPtr) {
        nvsnap_tracker_track_alloc((uintptr_t)*devPtr, size,
                                     nvsnap_get_current_device(), 1 /* MANAGED */);
        nvsnap_tracker_add_allocated_bytes((int64_t)size);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), size,
                               (uint64_t)(uintptr_t)*devPtr);
    }
    return err;
}

/* ─── cudaHostAlloc (pinned memory) ──────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaHostAlloc, void **, size_t, unsigned);

cudaError_t cudaHostAlloc(void **pHost, size_t size, unsigned flags)
{
    NVSNAP_LOAD_REAL(cudaHostAlloc);
    nvsnap_deferred_restore();

    cudaError_t err = real_cudaHostAlloc(pHost, size, flags);

    if (err == cudaSuccess && pHost && *pHost) {
        nvsnap_tracker_track_alloc((uintptr_t)*pHost, size,
                                     nvsnap_get_current_device(), 2 /* HOST_PINNED */);
        nvsnap_tracker_add_allocated_bytes((int64_t)size);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), size,
                               (uint64_t)(uintptr_t)*pHost);
    }
    return err;
}

/* ─── cudaFreeHost ───────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaFreeHost, void *);

cudaError_t cudaFreeHost(void *ptr)
{
    NVSNAP_LOAD_REAL(cudaFreeHost);

    if (ptr) {
        nvsnap_tracker_untrack_alloc((uintptr_t)ptr);
        nvsnap_metrics_write(NVSNAP_METRIC_FREE,
                               nvsnap_get_current_device(), 0,
                               (uint64_t)(uintptr_t)ptr);
    }

    return real_cudaFreeHost(ptr);
}

/* ─── cudaMallocAsync ────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaMallocAsync, void **, size_t, cudaStream_t);

cudaError_t cudaMallocAsync(void **devPtr, size_t size, cudaStream_t stream)
{
    NVSNAP_LOAD_REAL(cudaMallocAsync);
    nvsnap_deferred_restore();

    cudaError_t err = real_cudaMallocAsync(devPtr, size, stream);

    if (err == cudaSuccess && devPtr && *devPtr) {
        nvsnap_tracker_track_alloc((uintptr_t)*devPtr, size,
                                     nvsnap_get_current_device(), 0);
        nvsnap_tracker_add_allocated_bytes((int64_t)size);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), size,
                               (uint64_t)(uintptr_t)*devPtr);
    }
    return err;
}

/* ─── cudaFreeAsync ──────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaFreeAsync, void *, cudaStream_t);

cudaError_t cudaFreeAsync(void *devPtr, cudaStream_t stream)
{
    NVSNAP_LOAD_REAL(cudaFreeAsync);

    if (devPtr) {
        nvsnap_tracker_untrack_alloc((uintptr_t)devPtr);
        nvsnap_metrics_write(NVSNAP_METRIC_FREE,
                               nvsnap_get_current_device(), 0,
                               (uint64_t)(uintptr_t)devPtr);
    }

    return real_cudaFreeAsync(devPtr, stream);
}

/* ═══════════════════════════════════════════════════════════════════════
 * Device Management — needed for checkpoint coordination
 * ═══════════════════════════════════════════════════════════════════════ */

NVSNAP_DECLARE_REAL(cudaError_t, cudaSetDevice, int);

cudaError_t cudaSetDevice(int device)
{
    NVSNAP_LOAD_REAL(cudaSetDevice);
    nvsnap_deferred_restore();

    cudaError_t err = real_cudaSetDevice(device);

    if (err == cudaSuccess) {
        nvsnap_current_device = device;
        NVSNAP_GPU_LOG_DEBUG("cudaSetDevice(%d)", device);
    }
    return err;
}

NVSNAP_DECLARE_REAL(cudaError_t, cudaGetDevice, int *);

cudaError_t cudaGetDevice(int *device)
{
    NVSNAP_LOAD_REAL(cudaGetDevice);
    return real_cudaGetDevice(device);
}

/* ─── cudaDeviceSynchronize — needed for checkpoint quiesce ──────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaDeviceSynchronize, void);

cudaError_t cudaDeviceSynchronize(void)
{
    NVSNAP_LOAD_REAL(cudaDeviceSynchronize);
    nvsnap_deferred_restore();
    return real_cudaDeviceSynchronize();
}

/* ─── cudaMemGetInfo — pass through, vLLM queries this ───────────────── */

NVSNAP_DECLARE_REAL(cudaError_t, cudaMemGetInfo, size_t *, size_t *);

cudaError_t cudaMemGetInfo(size_t *free_mem, size_t *total_mem)
{
    NVSNAP_LOAD_REAL(cudaMemGetInfo);
    return real_cudaMemGetInfo(free_mem, total_mem);
}

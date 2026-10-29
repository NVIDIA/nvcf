/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Minimal CUDA Driver API interception for checkpoint/restore.
 *
 * Only memory allocation tracking and VMM functions needed for restore
 * are intercepted. All pass-through wrappers removed.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <string.h>

#include "nvsnap/gpu/cuda_types.h"
#include "nvsnap/gpu/interpose.h"
#include "nvsnap/gpu/tracker.h"
#include "nvsnap/gpu/metrics.h"

/* Defined in interpose_cudart.c */
extern void nvsnap_deferred_restore(void);
extern int nvsnap_get_current_device(void);

/* ═══════════════════════════════════════════════════════════════════════
 * Memory Allocation — tracked for checkpoint/restore
 * ═══════════════════════════════════════════════════════════════════════ */

/* ─── cuMemAlloc_v2 ──────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(CUresult, cuMemAlloc_v2, CUdeviceptr *, size_t);

CUresult cuMemAlloc_v2(CUdeviceptr *dptr, size_t bytesize)
{
    NVSNAP_LOAD_REAL(cuMemAlloc_v2);
    nvsnap_deferred_restore();

    CUresult err = real_cuMemAlloc_v2(dptr, bytesize);

    if (err == CUDA_SUCCESS && dptr) {
        nvsnap_tracker_track_alloc((uintptr_t)*dptr, bytesize,
                                     nvsnap_get_current_device(), 0);
        nvsnap_tracker_add_allocated_bytes((int64_t)bytesize);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), bytesize,
                               (uint64_t)*dptr);
        NVSNAP_GPU_LOG_DEBUG("cuMemAlloc_v2(%zu) -> 0x%llx", bytesize,
                           (unsigned long long)*dptr);
    }
    return err;
}

CUresult cuMemAlloc(CUdeviceptr *dptr, size_t bytesize)
{
    return cuMemAlloc_v2(dptr, bytesize);
}

/* ─── cuMemFree_v2 ───────────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(CUresult, cuMemFree_v2, CUdeviceptr);

CUresult cuMemFree_v2(CUdeviceptr dptr)
{
    NVSNAP_LOAD_REAL(cuMemFree_v2);

    if (dptr) {
        nvsnap_tracker_untrack_alloc((uintptr_t)dptr);
        nvsnap_metrics_write(NVSNAP_METRIC_FREE,
                               nvsnap_get_current_device(), 0, (uint64_t)dptr);
        NVSNAP_GPU_LOG_DEBUG("cuMemFree_v2(0x%llx)", (unsigned long long)dptr);
    }

    return real_cuMemFree_v2(dptr);
}

CUresult cuMemFree(CUdeviceptr dptr)
{
    return cuMemFree_v2(dptr);
}

/* ─── cuMemAllocManaged ──────────────────────────────────────────────── */

NVSNAP_DECLARE_REAL(CUresult, cuMemAllocManaged, CUdeviceptr *, size_t, unsigned);

CUresult cuMemAllocManaged(CUdeviceptr *dptr, size_t bytesize, unsigned flags)
{
    NVSNAP_LOAD_REAL(cuMemAllocManaged);
    nvsnap_deferred_restore();

    CUresult err = real_cuMemAllocManaged(dptr, bytesize, flags);

    if (err == CUDA_SUCCESS && dptr) {
        nvsnap_tracker_track_alloc((uintptr_t)*dptr, bytesize,
                                     nvsnap_get_current_device(), 1);
        nvsnap_tracker_add_allocated_bytes((int64_t)bytesize);
        nvsnap_metrics_write(NVSNAP_METRIC_ALLOC,
                               nvsnap_get_current_device(), bytesize,
                               (uint64_t)*dptr);
    }
    return err;
}

/* ═══════════════════════════════════════════════════════════════════════
 * VMM (Virtual Memory Management) — needed for restore
 * ═══════════════════════════════════════════════════════════════════════ */

NVSNAP_DECLARE_REAL(CUresult, cuMemAddressReserve, CUdeviceptr *, size_t,
                      size_t, CUdeviceptr, unsigned long long);

CUresult cuMemAddressReserve(CUdeviceptr *ptr, size_t size, size_t alignment,
                             CUdeviceptr addr, unsigned long long flags)
{
    NVSNAP_LOAD_REAL(cuMemAddressReserve);
    CUresult err = real_cuMemAddressReserve(ptr, size, alignment, addr, flags);
    if (err == CUDA_SUCCESS && ptr) {
        NVSNAP_GPU_LOG_DEBUG("cuMemAddressReserve(%zu) -> 0x%llx", size,
                           (unsigned long long)*ptr);
    }
    return err;
}

NVSNAP_DECLARE_REAL(CUresult, cuMemCreate, CUmemGenericAllocationHandle *,
                      size_t, const CUmemAllocationProp *, unsigned long long);

CUresult cuMemCreate(CUmemGenericAllocationHandle *handle, size_t size,
                     const CUmemAllocationProp *prop, unsigned long long flags)
{
    NVSNAP_LOAD_REAL(cuMemCreate);

    CUresult err = real_cuMemCreate(handle, size, prop, flags);
    if (err == CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_DEBUG("cuMemCreate(%zu) -> handle=%llu", size,
                           handle ? (unsigned long long)*handle : 0ULL);
    }
    return err;
}

NVSNAP_DECLARE_REAL(CUresult, cuMemMap, CUdeviceptr, size_t, size_t,
                      CUmemGenericAllocationHandle, unsigned long long);

CUresult cuMemMap(CUdeviceptr ptr, size_t size, size_t offset,
                  CUmemGenericAllocationHandle handle, unsigned long long flags)
{
    NVSNAP_LOAD_REAL(cuMemMap);
    CUresult err = real_cuMemMap(ptr, size, offset, handle, flags);

    if (err == CUDA_SUCCESS) {
        nvsnap_tracker_track_vmm_mapping((uintptr_t)ptr, size,
                                           (uint64_t)handle,
                                           nvsnap_get_current_device());
        nvsnap_metrics_write(NVSNAP_METRIC_VMM_MAP,
                               nvsnap_get_current_device(), size, (uint64_t)ptr);
        NVSNAP_GPU_LOG_DEBUG("cuMemMap(0x%llx, %zu)", (unsigned long long)ptr, size);
    }
    return err;
}

NVSNAP_DECLARE_REAL(CUresult, cuMemSetAccess, CUdeviceptr, size_t,
                      const CUmemAccessDesc *, size_t);

CUresult cuMemSetAccess(CUdeviceptr ptr, size_t size,
                        const CUmemAccessDesc *desc, size_t count)
{
    NVSNAP_LOAD_REAL(cuMemSetAccess);
    return real_cuMemSetAccess(ptr, size, desc, count);
}

NVSNAP_DECLARE_REAL(CUresult, cuMemUnmap, CUdeviceptr, size_t);

CUresult cuMemUnmap(CUdeviceptr ptr, size_t size)
{
    NVSNAP_LOAD_REAL(cuMemUnmap);

    nvsnap_tracker_untrack_vmm_mapping((uintptr_t)ptr);
    nvsnap_metrics_write(NVSNAP_METRIC_VMM_UNMAP,
                           nvsnap_get_current_device(), size, (uint64_t)ptr);

    return real_cuMemUnmap(ptr, size);
}

NVSNAP_DECLARE_REAL(CUresult, cuMemRelease, CUmemGenericAllocationHandle);

CUresult cuMemRelease(CUmemGenericAllocationHandle handle)
{
    NVSNAP_LOAD_REAL(cuMemRelease);
    NVSNAP_GPU_LOG_DEBUG("cuMemRelease(handle=%llu)", (unsigned long long)handle);
    return real_cuMemRelease(handle);
}

NVSNAP_DECLARE_REAL(CUresult, cuMemAddressFree, CUdeviceptr, size_t);

CUresult cuMemAddressFree(CUdeviceptr ptr, size_t size)
{
    NVSNAP_LOAD_REAL(cuMemAddressFree);
    NVSNAP_GPU_LOG_DEBUG("cuMemAddressFree(0x%llx, %zu)", (unsigned long long)ptr, size);
    return real_cuMemAddressFree(ptr, size);
}

/* ─── cuMemGetInfo_v2 — pass through, vLLM queries this ─────────────── */

NVSNAP_DECLARE_REAL(CUresult, cuMemGetInfo_v2, size_t *, size_t *);

CUresult cuMemGetInfo_v2(size_t *free_mem, size_t *total_mem)
{
    NVSNAP_LOAD_REAL(cuMemGetInfo_v2);
    return real_cuMemGetInfo_v2(free_mem, total_mem);
}

CUresult cuMemGetInfo(size_t *free_mem, size_t *total_mem)
{
    return cuMemGetInfo_v2(free_mem, total_mem);
}

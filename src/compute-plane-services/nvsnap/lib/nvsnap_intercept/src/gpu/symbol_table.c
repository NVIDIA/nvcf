/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — dlsym routing table.
 *
 * Maps symbol names to our wrapper functions. Only includes the minimal
 * set of functions needed for checkpoint/restore.
 *
 * LD_PRELOAD gives us symbol precedence for all exported functions.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <string.h>
#include <stdio.h>

#include "nvsnap/gpu/cuda_types.h"
#include "nvsnap/gpu/interpose.h"

/* ── Forward declarations of all wrapper functions ───────────────────── */

/* CUDA Runtime API — interpose_cudart.c */
extern cudaError_t cudaMalloc(void **, size_t);
extern cudaError_t cudaFree(void *);
extern cudaError_t cudaMallocManaged(void **, size_t, unsigned);
extern cudaError_t cudaHostAlloc(void **, size_t, unsigned);
extern cudaError_t cudaFreeHost(void *);
extern cudaError_t cudaMallocAsync(void **, size_t, cudaStream_t);
extern cudaError_t cudaFreeAsync(void *, cudaStream_t);
extern cudaError_t cudaSetDevice(int);
extern cudaError_t cudaGetDevice(int *);
extern cudaError_t cudaDeviceSynchronize(void);
extern cudaError_t cudaMemGetInfo(size_t *, size_t *);

/* CUDA Driver API — interpose_cudrv.c */
extern CUresult cuMemAlloc_v2(CUdeviceptr *, size_t);
extern CUresult cuMemAlloc(CUdeviceptr *, size_t);
extern CUresult cuMemFree_v2(CUdeviceptr);
extern CUresult cuMemFree(CUdeviceptr);
extern CUresult cuMemAllocManaged(CUdeviceptr *, size_t, unsigned);
extern CUresult cuMemAddressReserve(CUdeviceptr *, size_t, size_t, CUdeviceptr, unsigned long long);
extern CUresult cuMemCreate(CUmemGenericAllocationHandle *, size_t, const CUmemAllocationProp *, unsigned long long);
extern CUresult cuMemMap(CUdeviceptr, size_t, size_t, CUmemGenericAllocationHandle, unsigned long long);
extern CUresult cuMemSetAccess(CUdeviceptr, size_t, const CUmemAccessDesc *, size_t);
extern CUresult cuMemUnmap(CUdeviceptr, size_t);
extern CUresult cuMemRelease(CUmemGenericAllocationHandle);
extern CUresult cuMemAddressFree(CUdeviceptr, size_t);
extern CUresult cuMemGetInfo_v2(size_t *, size_t *);
extern CUresult cuMemGetInfo(size_t *, size_t *);

/* NCCL — interpose_nccl.c */
extern ncclResult_t ncclCommInitRank(ncclComm_t *, int, ncclUniqueId, int);
extern ncclResult_t ncclCommInitRankConfig(ncclComm_t *, int, ncclUniqueId, int, void *);
extern ncclResult_t ncclCommInitAll(ncclComm_t *, int, const int *);
extern ncclResult_t ncclCommDestroy(ncclComm_t);
extern ncclResult_t ncclCommAbort(ncclComm_t);

/* ── Symbol routing table ────────────────────────────────────────────── */

typedef struct {
    const char *name;
    void       *wrapper;
} SymbolEntry;

#define SYM(func) { #func, (void *)(func) }

static const SymbolEntry g_symbol_table[] = {
    /* CUDA Runtime API */
    SYM(cudaMalloc),
    SYM(cudaFree),
    SYM(cudaMallocManaged),
    SYM(cudaHostAlloc),
    SYM(cudaFreeHost),
    SYM(cudaMallocAsync),
    SYM(cudaFreeAsync),
    SYM(cudaSetDevice),
    SYM(cudaGetDevice),
    SYM(cudaDeviceSynchronize),
    SYM(cudaMemGetInfo),

    /* CUDA Driver API */
    SYM(cuMemAlloc_v2),
    SYM(cuMemAlloc),
    SYM(cuMemFree_v2),
    SYM(cuMemFree),
    SYM(cuMemAllocManaged),
    SYM(cuMemAddressReserve),
    SYM(cuMemCreate),
    SYM(cuMemMap),
    SYM(cuMemSetAccess),
    SYM(cuMemUnmap),
    SYM(cuMemRelease),
    SYM(cuMemAddressFree),
    SYM(cuMemGetInfo_v2),
    SYM(cuMemGetInfo),

    /* NCCL */
    SYM(ncclCommInitRank),
    SYM(ncclCommInitRankConfig),
    SYM(ncclCommInitAll),
    SYM(ncclCommDestroy),
    SYM(ncclCommAbort),

    { NULL, NULL }
};

#undef SYM

/* ── Lookup (used by internal code, e.g., tests) ───────────────────── */

void *nvsnap_lookup_symbol(const char *name)
{
    for (const SymbolEntry *e = g_symbol_table; e->name != NULL; e++) {
        if (strcmp(e->name, name) == 0) {
            return e->wrapper;
        }
    }
    return NULL;
}

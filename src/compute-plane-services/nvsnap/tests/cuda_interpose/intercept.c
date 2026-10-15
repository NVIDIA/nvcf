/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Minimal CUDA malloc/free interceptor for feasibility testing.
 *
 * Tracks all cudaMalloc/cudaFree calls. On command (via env var trigger),
 * saves all GPU memory to host files in /tmp/nvsnap-gpu-save/.
 *
 * Build:
 *   gcc -shared -fPIC -o libcuda_intercept.so intercept.c -ldl -lpthread
 *
 * Usage:
 *   LD_PRELOAD=./libcuda_intercept.so python3 test.py
 */

#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

/* CUDA types (no headers needed) */
typedef int cudaError_t;
typedef int CUresult;
typedef void *cudaStream_t;
typedef uint64_t CUdeviceptr;

#define CUDA_SUCCESS 0
#define MAX_ALLOCS 4096

typedef struct {
    void *ptr;           /* cudaMalloc pointer (void*) */
    CUdeviceptr cu_ptr;  /* cuMemAlloc pointer (uint64_t) */
    size_t size;
    int device;
    int is_cu;           /* 1 = cuMemAlloc, 0 = cudaMalloc */
    int freed;
} tracked_alloc_t;

static tracked_alloc_t g_allocs[MAX_ALLOCS];
static int g_nallocs = 0;
static pthread_mutex_t g_mutex = PTHREAD_MUTEX_INITIALIZER;

/* Real function pointers */
static cudaError_t (*real_cudaMalloc)(void **, size_t) = NULL;
static cudaError_t (*real_cudaFree)(void *) = NULL;
static cudaError_t (*real_cudaGetDevice)(int *) = NULL;
static CUresult (*real_cuMemAlloc_v2)(CUdeviceptr *, size_t) = NULL;
static CUresult (*real_cuMemFree_v2)(CUdeviceptr) = NULL;

static void *real_dlsym_fn(void *handle, const char *symbol) {
    static void *(*orig)(void *, const char *) = NULL;
    if (!orig)
        orig = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    return orig ? orig(handle, symbol) : NULL;
}

static void resolve(void) {
    if (!real_cudaMalloc)
        real_cudaMalloc = real_dlsym_fn(RTLD_NEXT, "cudaMalloc");
    if (!real_cudaFree)
        real_cudaFree = real_dlsym_fn(RTLD_NEXT, "cudaFree");
    if (!real_cudaGetDevice)
        real_cudaGetDevice = real_dlsym_fn(RTLD_NEXT, "cudaGetDevice");
    if (!real_cuMemAlloc_v2)
        real_cuMemAlloc_v2 = real_dlsym_fn(RTLD_NEXT, "cuMemAlloc_v2");
    if (!real_cuMemFree_v2)
        real_cuMemFree_v2 = real_dlsym_fn(RTLD_NEXT, "cuMemFree_v2");
}

static int get_device(void) {
    if (!real_cudaGetDevice) return 0;
    int dev = 0;
    real_cudaGetDevice(&dev);
    return dev;
}

/* ── Intercepted functions ─────────────────────────────────────────────── */

cudaError_t cudaMalloc(void **devPtr, size_t size) {
    resolve();
    cudaError_t ret = real_cudaMalloc(devPtr, size);
    if (ret == CUDA_SUCCESS && size > 0) {
        pthread_mutex_lock(&g_mutex);
        if (g_nallocs < MAX_ALLOCS) {
            tracked_alloc_t *a = &g_allocs[g_nallocs++];
            a->ptr = *devPtr;
            a->cu_ptr = 0;
            a->size = size;
            a->device = get_device();
            a->is_cu = 0;
            a->freed = 0;
        }
        pthread_mutex_unlock(&g_mutex);
    }
    return ret;
}

cudaError_t cudaFree(void *devPtr) {
    resolve();
    if (devPtr) {
        pthread_mutex_lock(&g_mutex);
        for (int i = 0; i < g_nallocs; i++) {
            if (!g_allocs[i].is_cu && g_allocs[i].ptr == devPtr && !g_allocs[i].freed) {
                g_allocs[i].freed = 1;
                break;
            }
        }
        pthread_mutex_unlock(&g_mutex);
    }
    return real_cudaFree(devPtr);
}

CUresult cuMemAlloc_v2(CUdeviceptr *dptr, size_t bytesize) {
    resolve();
    if (!real_cuMemAlloc_v2) {
        real_cuMemAlloc_v2 = real_dlsym_fn(RTLD_NEXT, "cuMemAlloc_v2");
        if (!real_cuMemAlloc_v2) return 1;
    }
    CUresult ret = real_cuMemAlloc_v2(dptr, bytesize);
    if (ret == CUDA_SUCCESS && bytesize > 0) {
        pthread_mutex_lock(&g_mutex);
        if (g_nallocs < MAX_ALLOCS) {
            tracked_alloc_t *a = &g_allocs[g_nallocs++];
            a->ptr = NULL;
            a->cu_ptr = *dptr;
            a->size = bytesize;
            a->device = get_device();
            a->is_cu = 1;
            a->freed = 0;
        }
        pthread_mutex_unlock(&g_mutex);
    }
    return ret;
}

CUresult cuMemFree_v2(CUdeviceptr dptr) {
    resolve();
    if (!real_cuMemFree_v2) {
        real_cuMemFree_v2 = real_dlsym_fn(RTLD_NEXT, "cuMemFree_v2");
        if (!real_cuMemFree_v2) return 1;
    }
    if (dptr) {
        pthread_mutex_lock(&g_mutex);
        for (int i = 0; i < g_nallocs; i++) {
            if (g_allocs[i].is_cu && g_allocs[i].cu_ptr == dptr && !g_allocs[i].freed) {
                g_allocs[i].freed = 1;
                break;
            }
        }
        pthread_mutex_unlock(&g_mutex);
    }
    return real_cuMemFree_v2(dptr);
}

/* ── Query interface (called from Python via ctypes) ───────────────────── */

int nvsnap_gpu_get_alloc_count(void) {
    return g_nallocs;
}

/* Returns: ptr, size, device, is_cu, freed */
int nvsnap_gpu_get_alloc(int index, uint64_t *ptr, uint64_t *size,
                        int *device, int *is_cu, int *freed) {
    if (index < 0 || index >= g_nallocs) return -1;
    tracked_alloc_t *a = &g_allocs[index];
    *ptr = a->is_cu ? a->cu_ptr : (uint64_t)a->ptr;
    *size = a->size;
    *device = a->device;
    *is_cu = a->is_cu;
    *freed = a->freed;
    return 0;
}

/* Get only live (non-freed) unique allocations.
 * Deduplicates by address (cudaMalloc and cuMemAlloc may share ranges). */
int nvsnap_gpu_get_live_allocs(uint64_t *ptrs, uint64_t *sizes, int *devices,
                              int max_count) {
    int count = 0;
    pthread_mutex_lock(&g_mutex);
    for (int i = 0; i < g_nallocs && count < max_count; i++) {
        if (g_allocs[i].freed) continue;
        uint64_t addr = g_allocs[i].is_cu ? g_allocs[i].cu_ptr : (uint64_t)g_allocs[i].ptr;
        /* Dedup: skip if we already have this address */
        int dup = 0;
        for (int j = 0; j < count; j++) {
            if (ptrs[j] == addr) { dup = 1; break; }
        }
        if (dup) continue;
        ptrs[count] = addr;
        sizes[count] = g_allocs[i].size;
        devices[count] = g_allocs[i].device;
        count++;
    }
    pthread_mutex_unlock(&g_mutex);
    return count;
}

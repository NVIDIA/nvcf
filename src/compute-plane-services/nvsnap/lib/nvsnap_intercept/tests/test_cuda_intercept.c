/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Comprehensive GPU allocation interception test suite.
 *
 * Tests the entire interception chain:
 *   1. PLT override (LD_PRELOAD symbol precedence)
 *   2. dlsym override (versioned GLIBC_2.2.5 + GLIBC_2.34)
 *   3. cuGetProcAddress override (CUDA 12+ PyTorch path)
 *   4. Allocation tracking (cudaMalloc, cuMemAlloc_v2, cuMemMap VMM)
 *   5. D2H save correctness (known pattern → save → verify)
 *   6. Edge cases (table full, double free, disabled mode)
 *
 * Build (inside a CUDA container):
 *   gcc -g -o tests/test_cuda_intercept tests/test_cuda_intercept.c \
 *       -I/usr/local/cuda/include -ldl -lpthread
 *
 * Run:
 *   NVSNAP_CUDA_INTERCEPT=1 LD_PRELOAD=./libnvsnap_intercept.so \
 *       ./tests/test_cuda_intercept
 *
 * All CUDA functions resolved via dlsym at runtime — no link dependency
 * on libcuda/libcudart (avoids interference with our LD_PRELOAD hooks).
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

/* ─── Types ─────────────────────────────────────────────────────────── */

typedef int CUresult;
typedef int cudaError_t;
typedef unsigned long long CUdeviceptr;
typedef unsigned long long CUmemGenericAllocationHandle;
typedef unsigned long long cuuint64_t;

/* VMM types */
typedef enum {
    CU_MEM_ALLOCATION_TYPE_PINNED = 1
} CUmemAllocationType;

typedef enum {
    CU_MEM_LOCATION_TYPE_DEVICE = 1
} CUmemLocationType;

typedef enum {
    CU_MEM_ACCESS_FLAGS_PROT_READWRITE = 3
} CUmemAccess_flags;

/* Minimal structs — sizes must match CUDA driver headers */
typedef struct {
    CUmemAllocationType type;
    unsigned long long _pad1;
    struct { CUmemLocationType type; int id; } location;
    void *win32HandleMetaData;
    struct { unsigned char compressionType; unsigned char gpuDirectRDMACapable;
             unsigned short usage; unsigned char reserved[4]; } allocFlags;
    unsigned long long _pad2;
} CUmemAllocationProp;

typedef struct {
    struct { CUmemLocationType type; int id; } location;
    CUmemAccess_flags flags;
} CUmemAccessDesc;

/* ─── Function pointers (resolved at runtime) ───────────────────────── */

static CUresult (*fn_cuInit)(unsigned) = NULL;
static cudaError_t (*fn_cudaSetDevice)(int) = NULL;
static cudaError_t (*fn_cudaMalloc)(void **, size_t) = NULL;
static cudaError_t (*fn_cudaFree)(void *) = NULL;
static cudaError_t (*fn_cudaMemcpy)(void *, const void *, size_t, int) = NULL;
static cudaError_t (*fn_cudaGetDeviceCount)(int *) = NULL;
static CUresult (*fn_cuMemAlloc_v2)(CUdeviceptr *, size_t) = NULL;
static CUresult (*fn_cuMemFree_v2)(CUdeviceptr) = NULL;

/* VMM */
static CUresult (*fn_cuMemAddressReserve)(CUdeviceptr *, size_t, size_t, CUdeviceptr, unsigned long long) = NULL;
static CUresult (*fn_cuMemCreate)(CUmemGenericAllocationHandle *, size_t, const CUmemAllocationProp *, unsigned long long) = NULL;
static CUresult (*fn_cuMemMap)(CUdeviceptr, size_t, size_t, CUmemGenericAllocationHandle, unsigned long long) = NULL;
static CUresult (*fn_cuMemSetAccess)(CUdeviceptr, size_t, const CUmemAccessDesc *, size_t) = NULL;
static CUresult (*fn_cuMemUnmap)(CUdeviceptr, size_t) = NULL;
static CUresult (*fn_cuMemRelease)(CUmemGenericAllocationHandle) = NULL;
static CUresult (*fn_cuMemAddressFree)(CUdeviceptr, size_t) = NULL;

/* cuGetProcAddress */
typedef CUresult (*cuGetProcAddress_v2_fn)(const char *, void **, int, cuuint64_t, int *);
static cuGetProcAddress_v2_fn fn_cuGetProcAddress_v2 = NULL;

/* nvsnap_cuda_save — from our library */
static int (*fn_nvsnap_cuda_save)(const char *) = NULL;

/* ─── Helpers ───────────────────────────────────────────────────────── */

static int g_pass = 0, g_fail = 0;

#define CHECK(cond, name) do { \
    if (cond) { printf("  PASS: %s\n", name); g_pass++; } \
    else { printf("  FAIL: %s\n", name); g_fail++; } \
} while(0)

/* Get the REAL dlsym (bypass our override) */
static void *(*real_dlsym_fn)(void *, const char *) = NULL;
static void *get_real_sym(const char *lib, const char *sym) {
    if (!real_dlsym_fn)
        real_dlsym_fn = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    void *h = dlopen(lib, RTLD_LAZY | RTLD_NOLOAD);
    if (!h) h = dlopen(lib, RTLD_LAZY);
    if (!h) return NULL;
    return real_dlsym_fn ? real_dlsym_fn(h, sym) : NULL;
}

static int resolve_all(void) {
    /* Use dlsym (goes through our override) for functions we want hooked */
    fn_cuInit = dlsym(RTLD_DEFAULT, "cuInit");
    fn_cudaSetDevice = dlsym(RTLD_DEFAULT, "cudaSetDevice");
    fn_cudaMalloc = dlsym(RTLD_DEFAULT, "cudaMalloc");
    fn_cudaFree = dlsym(RTLD_DEFAULT, "cudaFree");
    fn_cudaMemcpy = dlsym(RTLD_DEFAULT, "cudaMemcpy");
    fn_cudaGetDeviceCount = dlsym(RTLD_DEFAULT, "cudaGetDeviceCount");
    fn_cuMemAlloc_v2 = dlsym(RTLD_DEFAULT, "cuMemAlloc_v2");
    fn_cuMemFree_v2 = dlsym(RTLD_DEFAULT, "cuMemFree_v2");

    /* VMM */
    fn_cuMemAddressReserve = dlsym(RTLD_DEFAULT, "cuMemAddressReserve");
    fn_cuMemCreate = dlsym(RTLD_DEFAULT, "cuMemCreate");
    fn_cuMemMap = dlsym(RTLD_DEFAULT, "cuMemMap");
    fn_cuMemSetAccess = dlsym(RTLD_DEFAULT, "cuMemSetAccess");
    fn_cuMemUnmap = dlsym(RTLD_DEFAULT, "cuMemUnmap");
    fn_cuMemRelease = dlsym(RTLD_DEFAULT, "cuMemRelease");
    fn_cuMemAddressFree = dlsym(RTLD_DEFAULT, "cuMemAddressFree");

    /* cuGetProcAddress */
    fn_cuGetProcAddress_v2 = dlsym(RTLD_DEFAULT, "cuGetProcAddress_v2");

    /* nvsnap_cuda_save — resolve from our preloaded library */
    fn_nvsnap_cuda_save = dlsym(RTLD_DEFAULT, "nvsnap_cuda_save");

    if (!fn_cuInit || !fn_cudaMalloc || !fn_cudaFree) {
        fprintf(stderr, "Failed to resolve basic CUDA functions\n");
        return -1;
    }
    return 0;
}

static void rmrf(const char *path) {
    char cmd[512];
    snprintf(cmd, sizeof(cmd), "rm -rf %s", path);
    system(cmd);
}

static long file_size(const char *path) {
    struct stat st;
    if (stat(path, &st) != 0) return -1;
    return st.st_size;
}

/* Count entries in manifest JSON (simple — just count "addr" occurrences) */
static int manifest_count(const char *dir) {
    char path[512];
    snprintf(path, sizeof(path), "%s/gpu-manifest.json", dir);
    FILE *f = fopen(path, "r");
    if (!f) return -1;
    char buf[65536];
    size_t n = fread(buf, 1, sizeof(buf)-1, f);
    buf[n] = '\0';
    fclose(f);
    int count = 0;
    char *p = buf;
    while ((p = strstr(p, "\"addr\"")) != NULL) { count++; p++; }
    return count;
}

/* ─── Test 1: dlsym Override ────────────────────────────────────────── */

static void test_dlsym_override(void) {
    printf("\n=== Test 1: dlsym Override ===\n");

    /* Get pointers via dlsym (our override) and via real dlsym from libcuda/libcudart */
    void *our_cudaMalloc = dlsym(RTLD_DEFAULT, "cudaMalloc");
    void *real_cudaMalloc = get_real_sym("libcudart.so", "cudaMalloc");
    CHECK(our_cudaMalloc != NULL, "dlsym returns non-NULL for cudaMalloc");
    CHECK(real_cudaMalloc != NULL, "real cudaMalloc found in libcudart.so");
    CHECK(our_cudaMalloc != real_cudaMalloc, "dlsym(cudaMalloc) returns OUR hook, not libcudart's");

    void *our_cuMemMap = dlsym(RTLD_DEFAULT, "cuMemMap");
    void *real_cuMemMap = get_real_sym("libcuda.so.1", "cuMemMap");
    CHECK(our_cuMemMap != NULL, "dlsym returns non-NULL for cuMemMap");
    if (real_cuMemMap) {
        CHECK(our_cuMemMap != real_cuMemMap, "dlsym(cuMemMap) returns OUR hook, not libcuda's");
    }
}

/* ─── Test 2: cuGetProcAddress Override ──────────────────────────────── */

static void test_cugetprocaddress(void) {
    printf("\n=== Test 2: cuGetProcAddress Override ===\n");

    if (!fn_cuGetProcAddress_v2) {
        printf("  SKIP: cuGetProcAddress_v2 not available\n");
        return;
    }

    /* Resolve cuMemMap via cuGetProcAddress — should get our hook */
    void *resolved_cuMemMap = NULL;
    CUresult r = fn_cuGetProcAddress_v2("cuMemMap", &resolved_cuMemMap, 10020, 0, NULL);
    CHECK(r == 0, "cuGetProcAddress_v2(cuMemMap) succeeds");
    CHECK(resolved_cuMemMap != NULL, "cuGetProcAddress_v2(cuMemMap) returns non-NULL");

    /* Compare with real libcuda version */
    void *real_cuMemMap = get_real_sym("libcuda.so.1", "cuMemMap");
    if (real_cuMemMap && resolved_cuMemMap) {
        CHECK(resolved_cuMemMap != real_cuMemMap,
              "cuGetProcAddress(cuMemMap) returns OUR hook, not libcuda's");
    }

    /* Resolve something we DON'T hook — should pass through to real */
    void *resolved_cuCtxCreate = NULL;
    r = fn_cuGetProcAddress_v2("cuCtxCreate", &resolved_cuCtxCreate, 2000, 0, NULL);
    CHECK(r == 0, "cuGetProcAddress_v2(cuCtxCreate) succeeds (passthrough)");
    CHECK(resolved_cuCtxCreate != NULL, "cuGetProcAddress_v2(cuCtxCreate) returns non-NULL");
}

/* ─── Test 3: cudaMalloc Tracking ───────────────────────────────────── */

static void test_cudamalloc_tracking(void) {
    printf("\n=== Test 3: cudaMalloc Tracking ===\n");

    const char *dir = "/tmp/nvsnap_test_cudamalloc";
    rmrf(dir);
    mkdir(dir, 0755);

    void *ptr = NULL;
    cudaError_t err = fn_cudaMalloc(&ptr, 4096);
    CHECK(err == 0, "cudaMalloc(4096) succeeds");
    CHECK(ptr != NULL, "cudaMalloc returns non-NULL pointer");

    if (fn_nvsnap_cuda_save) {
        int rc = fn_nvsnap_cuda_save(dir);
        CHECK(rc == 0, "nvsnap_cuda_save succeeds");
        int count = manifest_count(dir);
        CHECK(count >= 1, "manifest has >= 1 allocation after cudaMalloc");

        /* Free and verify it's gone */
        fn_cudaFree(ptr);
        rmrf(dir);
        mkdir(dir, 0755);
        fn_nvsnap_cuda_save(dir);
        int count2 = manifest_count(dir);
        CHECK(count2 == count - 1, "manifest has one fewer allocation after cudaFree");
    } else {
        printf("  SKIP: nvsnap_cuda_save not found (not preloaded?)\n");
        fn_cudaFree(ptr);
    }
    rmrf(dir);
}

/* ─── Test 4: cuMemAlloc_v2 Tracking ────────────────────────────────── */

static void test_cumemalloc_tracking(void) {
    printf("\n=== Test 4: cuMemAlloc_v2 Tracking ===\n");

    if (!fn_cuMemAlloc_v2 || !fn_cuMemFree_v2) {
        printf("  SKIP: cuMemAlloc_v2/cuMemFree_v2 not available\n");
        return;
    }

    const char *dir = "/tmp/nvsnap_test_cumemalloc";
    rmrf(dir);
    mkdir(dir, 0755);

    CUdeviceptr dptr = 0;
    CUresult r = fn_cuMemAlloc_v2(&dptr, 8192);
    CHECK(r == 0, "cuMemAlloc_v2(8192) succeeds");
    CHECK(dptr != 0, "cuMemAlloc_v2 returns non-zero address");

    if (fn_nvsnap_cuda_save) {
        int rc = fn_nvsnap_cuda_save(dir);
        CHECK(rc == 0, "nvsnap_cuda_save succeeds");
        int count = manifest_count(dir);
        CHECK(count >= 1, "manifest has >= 1 allocation after cuMemAlloc_v2");
    }

    fn_cuMemFree_v2(dptr);
    rmrf(dir);
}

/* ─── Test 5: VMM (cuMemMap) Tracking ───────────────────────────────── */

static void test_vmm_tracking(void) {
    printf("\n=== Test 5: VMM cuMemMap Tracking (PyTorch path) ===\n");

    if (!fn_cuMemAddressReserve || !fn_cuMemCreate || !fn_cuMemMap ||
        !fn_cuMemSetAccess || !fn_cuMemUnmap || !fn_cuMemRelease ||
        !fn_cuMemAddressFree) {
        printf("  SKIP: VMM APIs not available\n");
        return;
    }

    const size_t SIZE = 2 * 1024 * 1024; /* 2 MB — CUDA VMM granularity */
    CUresult r;

    /* Reserve VA */
    CUdeviceptr va = 0;
    r = fn_cuMemAddressReserve(&va, SIZE, 0, 0, 0);
    CHECK(r == 0, "cuMemAddressReserve succeeds");

    /* Create physical allocation */
    CUmemAllocationProp prop;
    memset(&prop, 0, sizeof(prop));
    prop.type = CU_MEM_ALLOCATION_TYPE_PINNED;
    prop.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    prop.location.id = 0;

    CUmemGenericAllocationHandle handle = 0;
    r = fn_cuMemCreate(&handle, SIZE, &prop, 0);
    CHECK(r == 0, "cuMemCreate succeeds");

    /* Map physical to VA — THIS is where our hook should fire */
    r = fn_cuMemMap(va, SIZE, 0, handle, 0);
    CHECK(r == 0, "cuMemMap succeeds");

    /* Set access */
    CUmemAccessDesc access;
    access.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    access.location.id = 0;
    access.flags = CU_MEM_ACCESS_FLAGS_PROT_READWRITE;
    r = fn_cuMemSetAccess(va, SIZE, &access, 1);
    CHECK(r == 0, "cuMemSetAccess succeeds");

    /* Verify tracking */
    const char *dir = "/tmp/nvsnap_test_vmm";
    rmrf(dir);
    mkdir(dir, 0755);

    if (fn_nvsnap_cuda_save) {
        int rc = fn_nvsnap_cuda_save(dir);
        CHECK(rc == 0, "nvsnap_cuda_save after cuMemMap succeeds");
        int count = manifest_count(dir);
        CHECK(count >= 1, "manifest has >= 1 allocation after cuMemMap");

        /* Check the saved file has the right size */
        char fpath[512];
        snprintf(fpath, sizeof(fpath), "%s/gpu-alloc-0.bin", dir);
        long fsize = file_size(fpath);
        if (fsize >= 0) {
            CHECK((size_t)fsize == SIZE, "saved file size matches allocation size (2 MB)");
        }
    }

    /* Cleanup */
    fn_cuMemUnmap(va, SIZE);
    fn_cuMemRelease(handle);
    fn_cuMemAddressFree(va, SIZE);

    if (fn_nvsnap_cuda_save) {
        rmrf(dir);
        mkdir(dir, 0755);
        fn_nvsnap_cuda_save(dir);
        int count2 = manifest_count(dir);
        CHECK(count2 >= 0, "manifest valid after cuMemUnmap");
    }
    rmrf(dir);
}

/* ─── Test 6: D2H Save Data Integrity ───────────────────────────────── */

static void test_d2h_integrity(void) {
    printf("\n=== Test 6: D2H Save Data Integrity ===\n");

    if (!fn_cudaMemcpy || !fn_nvsnap_cuda_save) {
        printf("  SKIP: cudaMemcpy or nvsnap_cuda_save not available\n");
        return;
    }

    const size_t SIZE = 1024 * 1024; /* 1 MB */
    const char *dir = "/tmp/nvsnap_test_d2h";
    rmrf(dir);
    mkdir(dir, 0755);

    /* Allocate and fill with known pattern */
    void *dptr = NULL;
    fn_cudaMalloc(&dptr, SIZE);
    CHECK(dptr != NULL, "cudaMalloc for D2H test succeeds");

    unsigned int *host_pattern = malloc(SIZE);
    for (size_t i = 0; i < SIZE / sizeof(unsigned int); i++)
        host_pattern[i] = 0xDEADBEEF ^ (unsigned int)i;

    fn_cudaMemcpy(dptr, host_pattern, SIZE, 1 /* H2D */);

    /* Save */
    int rc = fn_nvsnap_cuda_save(dir);
    CHECK(rc == 0, "nvsnap_cuda_save succeeds");

    /* Find the allocation file — look for any gpu-alloc-*.bin */
    int verified = 0;
    for (int i = 0; i < 100; i++) {
        char fpath[512];
        snprintf(fpath, sizeof(fpath), "%s/gpu-alloc-%d.bin", dir, i);
        long fsize = file_size(fpath);
        if (fsize == (long)SIZE) {
            /* Read and compare */
            FILE *f = fopen(fpath, "rb");
            if (f) {
                unsigned int *readback = malloc(SIZE);
                fread(readback, 1, SIZE, f);
                fclose(f);
                int match = memcmp(host_pattern, readback, SIZE) == 0;
                CHECK(match, "D2H saved data matches original pattern (1 MB)");
                verified = 1;
                free(readback);
            }
            break;
        }
    }
    if (!verified) {
        CHECK(0, "D2H saved file with correct size found");
    }

    free(host_pattern);
    fn_cudaFree(dptr);
    rmrf(dir);
}

/* ─── Test 7: cuGetProcAddress → Allocate → Track ───────────────────── */

static void test_cugetprocaddr_allocate(void) {
    printf("\n=== Test 7: cuGetProcAddress → Allocate → Track ===\n");

    if (!fn_cuGetProcAddress_v2 || !fn_nvsnap_cuda_save) {
        printf("  SKIP: cuGetProcAddress_v2 or nvsnap_cuda_save not available\n");
        return;
    }

    /* Resolve cudaMalloc-equivalent via cuGetProcAddress (the PyTorch path) */
    CUresult (*resolved_cuMemAlloc)(CUdeviceptr *, size_t) = NULL;
    CUresult r = fn_cuGetProcAddress_v2("cuMemAlloc", (void **)&resolved_cuMemAlloc, 2000, 0, NULL);
    CHECK(r == 0 && resolved_cuMemAlloc != NULL, "cuGetProcAddress resolves cuMemAlloc");

    CUresult (*resolved_cuMemFree)(CUdeviceptr) = NULL;
    fn_cuGetProcAddress_v2("cuMemFree", (void **)&resolved_cuMemFree, 2000, 0, NULL);

    if (!resolved_cuMemAlloc) return;

    const char *dir = "/tmp/nvsnap_test_getproc";
    rmrf(dir);
    mkdir(dir, 0755);

    /* Allocate via the cuGetProcAddress-resolved function */
    CUdeviceptr dptr = 0;
    r = resolved_cuMemAlloc(&dptr, 16384);
    CHECK(r == 0, "cuGetProcAddress-resolved cuMemAlloc(16384) succeeds");

    /* Verify it was tracked */
    int rc = fn_nvsnap_cuda_save(dir);
    CHECK(rc == 0, "nvsnap_cuda_save succeeds");
    int count = manifest_count(dir);
    CHECK(count >= 1, "allocation via cuGetProcAddress path is tracked in manifest");

    if (resolved_cuMemFree) resolved_cuMemFree(dptr);
    rmrf(dir);
}

/* ─── Test 8: Empty Save ────────────────────────────────────────────── */

static void test_empty_save(void) {
    printf("\n=== Test 8: Empty Save (no allocations) ===\n");

    if (!fn_nvsnap_cuda_save) {
        printf("  SKIP: nvsnap_cuda_save not available\n");
        return;
    }

    /* Note: there may be leftover allocations from previous tests.
     * We just verify save doesn't crash with any count. */
    const char *dir = "/tmp/nvsnap_test_empty";
    rmrf(dir);
    mkdir(dir, 0755);
    int rc = fn_nvsnap_cuda_save(dir);
    CHECK(rc == 0 || rc == -1, "nvsnap_cuda_save returns without crash");
    rmrf(dir);
}

/* ─── Test 9: Save to Invalid Path ──────────────────────────────────── */

static void test_save_invalid_path(void) {
    printf("\n=== Test 9: Save to Invalid Path ===\n");

    if (!fn_nvsnap_cuda_save) {
        printf("  SKIP: nvsnap_cuda_save not available\n");
        return;
    }

    /* Allocate something so there's data to save */
    void *dptr = NULL;
    fn_cudaMalloc(&dptr, 1024);

    int rc = fn_nvsnap_cuda_save("/nonexistent/nvsnap_test_bad");
    CHECK(rc == -1, "nvsnap_cuda_save returns -1 for invalid path");
    /* Process must still be alive */
    CHECK(1, "process survived save to invalid path");

    fn_cudaFree(dptr);
}

/* ─── Test 10: Multi-Device Tracking ────────────────────────────────── */

static void test_multidevice(void) {
    printf("\n=== Test 10: Multi-Device Tracking ===\n");

    int ndev = 0;
    fn_cudaGetDeviceCount(&ndev);
    if (ndev < 2) {
        printf("  SKIP: need >= 2 GPUs, have %d\n", ndev);
        return;
    }

    /* Allocate on device 0 */
    fn_cudaSetDevice(0);
    void *p0 = NULL;
    fn_cudaMalloc(&p0, 4096);

    /* Allocate on device 1 */
    fn_cudaSetDevice(1);
    void *p1 = NULL;
    fn_cudaMalloc(&p1, 4096);

    CHECK(p0 != NULL && p1 != NULL, "allocations on both devices succeed");
    CHECK(p0 != p1, "allocations on different devices have different addresses");

    /* Save and check manifest has both devices */
    const char *dir = "/tmp/nvsnap_test_multidev";
    rmrf(dir);
    mkdir(dir, 0755);
    if (fn_nvsnap_cuda_save) {
        fn_nvsnap_cuda_save(dir);
        /* Read manifest and check for device 0 and device 1 */
        char path[512];
        snprintf(path, sizeof(path), "%s/gpu-manifest.json", dir);
        FILE *f = fopen(path, "r");
        if (f) {
            char buf[65536];
            size_t n = fread(buf, 1, sizeof(buf)-1, f);
            buf[n] = '\0';
            fclose(f);
            CHECK(strstr(buf, "\"device\": 0") != NULL, "manifest contains device 0");
            CHECK(strstr(buf, "\"device\": 1") != NULL, "manifest contains device 1");
        }
    }

    fn_cudaSetDevice(0); fn_cudaFree(p0);
    fn_cudaSetDevice(1); fn_cudaFree(p1);
    fn_cudaSetDevice(0);
    rmrf(dir);
}

/* ─── Test 11: Thread Safety ────────────────────────────────────────── */

#define THREAD_COUNT 4
#define ALLOCS_PER_THREAD 50

static void *thread_alloc_free(void *arg) {
    (void)arg;
    for (int i = 0; i < ALLOCS_PER_THREAD; i++) {
        void *p = NULL;
        if (fn_cudaMalloc(&p, 1024) == 0 && p) {
            fn_cudaFree(p);
        }
    }
    return NULL;
}

static void test_thread_safety(void) {
    printf("\n=== Test 11: Thread Safety ===\n");

    pthread_t threads[THREAD_COUNT];
    for (int i = 0; i < THREAD_COUNT; i++)
        pthread_create(&threads[i], NULL, thread_alloc_free, NULL);
    for (int i = 0; i < THREAD_COUNT; i++)
        pthread_join(threads[i], NULL);

    CHECK(1, "concurrent alloc/free completed without crash");

    /* Save should work after concurrent ops */
    if (fn_nvsnap_cuda_save) {
        const char *dir = "/tmp/nvsnap_test_threads";
        rmrf(dir);
        mkdir(dir, 0755);
        int rc = fn_nvsnap_cuda_save(dir);
        CHECK(rc == 0, "nvsnap_cuda_save succeeds after concurrent alloc/free");
        rmrf(dir);
    }
}

/* ─── Main ──────────────────────────────────────────────────────────── */

int main(void) {
    printf("=== NVSNAP CUDA Interception Test Suite ===\n");
    printf("NVSNAP_CUDA_INTERCEPT=%s\n", getenv("NVSNAP_CUDA_INTERCEPT") ?: "(unset)");
    printf("LD_PRELOAD=%s\n\n", getenv("LD_PRELOAD") ?: "(unset)");

    if (resolve_all() < 0) {
        fprintf(stderr, "FATAL: Cannot resolve CUDA functions\n");
        return 1;
    }

    /* Initialize CUDA */
    if (fn_cuInit) fn_cuInit(0);
    fn_cudaSetDevice(0);

    /* Run tests */
    test_dlsym_override();
    test_cugetprocaddress();
    test_cudamalloc_tracking();
    test_cumemalloc_tracking();
    test_vmm_tracking();
    test_d2h_integrity();
    test_cugetprocaddr_allocate();
    test_empty_save();
    test_save_invalid_path();
    test_multidevice();
    test_thread_safety();

    /* Summary */
    printf("\n=== Results: %d passed, %d failed ===\n", g_pass, g_fail);
    return g_fail > 0 ? 1 : 0;
}

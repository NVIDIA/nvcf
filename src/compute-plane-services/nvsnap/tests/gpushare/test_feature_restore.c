/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * Probe: which CUDA features survive checkpoint/restore on this platform?
 *
 * A child process sets up one feature, then the parent runs
 * nvsnap-gpu-suspend full-save + full-restore on it, and the child checks
 * the feature still works. One feature per run:
 *
 *   alloc      cuMemAlloc, memset, read back
 *   hostalloc  cuMemHostAlloc(DEVICEMAP|PORTABLE), GPU writes, CPU reads
 *   hostreg    malloc + cuMemHostRegister(DEVICEMAP), GPU writes, CPU reads
 *   memop      cuStreamWriteValue32 to pinned host memory
 *   hostnuma   cuMemCreate on CU_MEM_LOCATION_TYPE_HOST_NUMA, mapped for GPU
 *   vmm        cuMemCreate on the device, shareable as a POSIX fd
 *   fabric     cuMemCreate on the device, shareable as a fabric handle (IMEX;
 *              NCCL allocates so when an IMEX channel is present)
 *   vmmexp, fabricexp   as vmm / fabric, and exported (as a peer would get it)
 *   vmmpeer    as vmm, with peer access to device dev+1 enabled
 *   kernel     JIT a PTX kernel, launch it (before and after)
 *   mcast      multicast object (NVLS) of all visible devices: create, add
 *              devices, bind, map, tear down (before and after; nothing
 *              held across)
 *
 * With "-rel" (hostreg-rel, hostnuma-rel) the child drops the feature before
 * checkpoint and re-creates it at the same address after restore, keeping
 * the contents: the workaround for features the driver cannot restore.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_feature_restore \
 *            test_feature_restore.c -L/usr/local/cuda/lib64/stubs -lcuda
 * Run:   ./test_feature_restore <nvsnap-gpu-suspend> <feature> [dev=0]
 */
#include <cuda.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE (64UL << 20)
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

static const char *ptx =
    ".version 7.0\n.target sm_52\n.address_size 64\n"
    ".visible .entry fill(.param .u64 p, .param .u32 v) {\n"
    "  .reg .b64 a; .reg .b32 x;\n"
    "  ld.param.u64 a, [p]; ld.param.u32 x, [v];\n"
    "  cvta.to.global.u64 a, a; st.global.u32 [a], x; ret;\n}\n";

struct st {
    CUdeviceptr d; uint32_t *h; CUfunction fn; CUstream s;
    CUmemGenericAllocationHandle mh; CUmemAllocationProp prop; CUmemAccessDesc acc[2]; void *save;
};

static void setup(const char *f, struct st *t, CUdevice dev)
{
    if (!strcmp(f, "alloc")) {
        CK(cuMemAlloc(&t->d, SIZE));
    } else if (!strcmp(f, "hostalloc") || !strcmp(f, "memop")) {
        CK(cuMemHostAlloc((void **)&t->h, SIZE, CU_MEMHOSTALLOC_DEVICEMAP | CU_MEMHOSTALLOC_PORTABLE));
        CK(cuMemHostGetDevicePointer(&t->d, t->h, 0));
    } else if (!strcmp(f, "hostreg")) {
        if (posix_memalign((void **)&t->h, 1 << 16, SIZE)) exit(2);
        CK(cuMemHostRegister(t->h, SIZE, CU_MEMHOSTREGISTER_DEVICEMAP));
        CK(cuMemHostGetDevicePointer(&t->d, t->h, 0));
    } else if (!strcmp(f, "hostnuma")) {
        int numa = 0;
        CK(cuDeviceGetAttribute(&numa, CU_DEVICE_ATTRIBUTE_HOST_NUMA_ID, dev));
        CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED,
                                  .location = { CU_MEM_LOCATION_TYPE_HOST_NUMA, numa } };
        size_t g;
        CK(cuMemGetAllocationGranularity(&g, &p, CU_MEM_ALLOC_GRANULARITY_MINIMUM));
        CK(cuMemCreate(&t->mh, SIZE, &p, 0));
        CK(cuMemAddressReserve(&t->d, SIZE, g, 0, 0));
        CK(cuMemMap(t->d, SIZE, 0, t->mh, 0));
        CUmemAccessDesc a[2] = { { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE },
                                 { { CU_MEM_LOCATION_TYPE_HOST_NUMA, numa }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE } };
        CK(cuMemSetAccess(t->d, SIZE, a, 2));
        t->prop = p;
        memcpy(t->acc, a, sizeof(a));
        t->h = (uint32_t *)(uintptr_t)t->d;
    } else if (!strncmp(f, "vmm", 3) || !strncmp(f, "fabric", 6)) {
        int fab = !strncmp(f, "fabric", 6);
        CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED,
                                  .location = { CU_MEM_LOCATION_TYPE_DEVICE, dev },
                                  .requestedHandleTypes = fab ? CU_MEM_HANDLE_TYPE_FABRIC
                                                              : CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR };
        size_t g;
        CK(cuMemGetAllocationGranularity(&g, &p, CU_MEM_ALLOC_GRANULARITY_MINIMUM));
        CK(cuMemCreate(&t->mh, SIZE, &p, 0));
        CK(cuMemAddressReserve(&t->d, SIZE, g, 0, 0));
        CK(cuMemMap(t->d, SIZE, 0, t->mh, 0));
        CUmemAccessDesc a = { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
        CK(cuMemSetAccess(t->d, SIZE, &a, 1));
        if (strstr(f, "peer")) {
            CUcontext cur, pc;
            CUdevice pd;
            CK(cuCtxGetCurrent(&cur));
            CK(cuDeviceGet(&pd, dev + 1));
            CK(cuDevicePrimaryCtxRetain(&pc, pd));
            CK(cuCtxEnablePeerAccess(pc, 0));
            CK(cuCtxSetCurrent(pc));
            CK(cuCtxEnablePeerAccess(cur, 0));
            CK(cuCtxSetCurrent(cur));
        }
        if (strstr(f, "exp")) {
            CUmemFabricHandle fh;
            int fd;
            CK(cuMemExportToShareableHandle(fab ? (void *)&fh : (void *)&fd, t->mh, p.requestedHandleTypes, 0));
            if (!fab) close(fd);
        }
    } else if (!strcmp(f, "mcast")) {
        /* nothing held across the checkpoint */
    } else if (!strcmp(f, "kernel")) {
        CUmodule m;
        CK(cuMemAlloc(&t->d, SIZE));
        CK(cuModuleLoadData(&m, ptx));
        CK(cuModuleGetFunction(&t->fn, m, "fill"));
    } else {
        fprintf(stderr, "unknown feature %s\n", f); exit(1);
    }
    CK(cuStreamCreate(&t->s, CU_STREAM_NON_BLOCKING));
}

/* Multicast group of every visible device: every step must succeed. */
static int mcast(void)
{
    int n;
    CK(cuDeviceGetCount(&n));
    if (n > 8) n = 8;
    /* MC_FABRIC=1: fabric handles (what NCCL uses with an IMEX channel) */
    CUmemAllocationHandleType ht = getenv("MC_FABRIC") ? CU_MEM_HANDLE_TYPE_FABRIC : CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
    CUmulticastObjectProp mp = { .numDevices = n, .handleTypes = ht };
    size_t g;
    CK(cuMulticastGetGranularity(&g, &mp, CU_MULTICAST_GRANULARITY_RECOMMENDED));
    mp.size = g;
    CUmemGenericAllocationHandle mc, uc[8];
    CUdeviceptr va;
    CUmemAccessDesc a[8];
    CK(cuMulticastCreate(&mc, &mp));
    for (int d = 0; d < n; d++) {
        int sup = -1;
        cuDeviceGetAttribute(&sup, CU_DEVICE_ATTRIBUTE_MULTICAST_SUPPORTED, d);
        CUresult r = cuMulticastAddDevice(mc, d);
        if (r != CUDA_SUCCESS && getenv("MC_RETAIN")) {  /* retry with the device's context retained */
            CUcontext c;
            CK(cuDevicePrimaryCtxRetain(&c, d));
            r = cuMulticastAddDevice(mc, d);
        }
        if (r != CUDA_SUCCESS) {
            const char *e = "?";
            cuGetErrorString(r, &e);
            fprintf(stderr, "[%d] cuMulticastAddDevice(dev %d, multicast supported=%d): %s\n", getpid(), d, sup, e);
            exit(2);
        }
    }
    for (int d = 0; d < n; d++) {
        CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED, .location = { CU_MEM_LOCATION_TYPE_DEVICE, d },
                                  .requestedHandleTypes = ht };
        CK(cuMemCreate(&uc[d], g, &p, 0));
        CK(cuMulticastBindMem(mc, 0, uc[d], 0, g, 0));
        a[d] = (CUmemAccessDesc){ { CU_MEM_LOCATION_TYPE_DEVICE, d }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
    }
    CK(cuMemAddressReserve(&va, g, g, 0, 0));
    CK(cuMemMap(va, g, 0, mc, 0));
    CK(cuMemSetAccess(va, g, a, n));
    CK(cuMemUnmap(va, g));
    CK(cuMemAddressFree(va, g));
    for (int d = 0; d < n; d++) {
        CK(cuMulticastUnbind(mc, d, 0, g));
        CK(cuMemRelease(uc[d]));
    }
    CK(cuMemRelease(mc));
    return 0;
}

/* Use the feature to store v; return 0 if v reads back. */
static int use(const char *f, struct st *t, uint32_t v)
{
    uint32_t got = 0;
    if (!strcmp(f, "mcast")) return mcast();
    if (!strcmp(f, "memop")) {
        CK(cuStreamWriteValue32(t->s, t->d, v, 0));
    } else if (!strcmp(f, "kernel")) {
        void *args[] = { &t->d, &v };
        CK(cuLaunchKernel(t->fn, 1, 1, 1, 1, 1, 1, 0, t->s, args, NULL));
    } else {
        CK(cuMemsetD32Async(t->d, v, SIZE / 4, t->s));
    }
    CK(cuStreamSynchronize(t->s));
    if (t->h) got = t->h[0];
    else CK(cuMemcpyDtoH(&got, t->d, 4));
    if (got != v) fprintf(stderr, "[%d] %s: read %#x, want %#x\n", getpid(), f, got, v);
    return got != v;
}

/* "-rel": drop the feature before checkpoint (contents stay in host RAM). */
static void drop(const char *f, struct st *t)
{
    if (!strcmp(f, "hostreg")) {
        CK(cuMemHostUnregister(t->h));
    } else if (!strcmp(f, "hostnuma")) {
        t->save = malloc(SIZE);
        memcpy(t->save, t->h, SIZE);
        CK(cuMemUnmap(t->d, SIZE));
        CK(cuMemRelease(t->mh));
    }
}

/* ... and re-create it at the same address after restore; 0 if intact. */
static int redo(const char *f, struct st *t)
{
    CUdeviceptr d = 0;
    if (!strcmp(f, "hostreg")) {
        CK(cuMemHostRegister(t->h, SIZE, CU_MEMHOSTREGISTER_DEVICEMAP));
        CK(cuMemHostGetDevicePointer(&d, t->h, 0));
    } else if (!strcmp(f, "hostnuma")) {
        CK(cuMemCreate(&t->mh, SIZE, &t->prop, 0));
        CK(cuMemMap(t->d, SIZE, 0, t->mh, 0));
        CK(cuMemSetAccess(t->d, SIZE, t->acc, 2));
        memcpy(t->h, t->save, SIZE);
        d = t->d;
    }
    if (d != t->d) { fprintf(stderr, "[%d] %s: device pointer moved %#llx -> %#llx\n", getpid(), f,
                             (unsigned long long)t->d, (unsigned long long)d); return 1; }
    if (t->h[0] != 0x1234 || t->h[SIZE / 4 - 1] != 0x1234) { fprintf(stderr, "[%d] %s: contents lost\n", getpid(), f); return 1; }
    return 0;
}

static int run(const char *tool, const char *action, pid_t pid)
{
    char cmd[512];
    snprintf(cmd, sizeof cmd, "%s %s %d", tool, action, pid);
    int st = system(cmd);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

int main(int argc, char **argv)
{
    if (argc < 3) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> <feature> [dev]\n", argv[0]); return 1; }
    const char *tool = argv[1], *name = argv[2];
    char f[32];
    snprintf(f, sizeof f, "%s", name);
    int rel = strlen(f) > 4 && !strcmp(f + strlen(f) - 4, "-rel");
    if (rel) f[strlen(f) - 4] = 0;
    int devno = argc > 3 ? atoi(argv[3]) : 0;
    int ready[2], go[2];
    if (pipe(ready) || pipe(go)) return 1;
    pid_t pid = fork();
    if (!pid) {
        CUdevice dev; CUcontext ctx; struct st t = {0}; char c = 0;
        CK(cuInit(0)); CK(cuDeviceGet(&dev, devno));
        CK(cuDevicePrimaryCtxRetain(&ctx, dev)); CK(cuCtxSetCurrent(ctx));
        setup(f, &t, dev);
        c = use(f, &t, 0x1234);
        if (rel) { CK(cuCtxSynchronize()); drop(f, &t); }
        if (write(ready[1], &c, 1) != 1 || read(go[0], &c, 1) != 1) exit(2);
        if (rel && redo(f, &t)) exit(3);
        exit(use(f, &t, 0x5678) ? 3 : 0);
    }
    char c;
    if (read(ready[0], &c, 1) != 1 || c) { fprintf(stderr, "%s: setup failed\n", name); return 1; }
    int save = run(tool, "full-save", pid), restore = save ? -1 : run(tool, "full-restore", pid);
    int st = 0;
    if (!restore) { if (write(go[1], &c, 1) != 1) return 1; waitpid(pid, &st, 0); }
    else kill(pid, SIGKILL);
    int ok = !save && !restore && WIFEXITED(st) && WEXITSTATUS(st) == 0;
    printf("%-12s save=%d restore=%d use-after=%s => %s\n", name, save, restore,
           restore ? "-" : (WEXITSTATUS(st) ? "FAIL" : "ok"), ok ? "PASS" : "FAIL");
    return !ok;
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/*
 * A stand-in for libcuda.so.1 with the driver entry points
 * `nvsnap-gpu-suspend probe` uses (gpu_probe_test.go). The GPU named by
 * CUDA_VISIBLE_DEVICES picks how it fails:
 *   GPU-ok        every step succeeds
 *   GPU-init      cuInit: CUDA_ERROR_UNKNOWN
 *   GPU-none      no device
 *   GPU-two       two devices (the caller named none)
 *   GPU-ecc       context: CUDA_ERROR_ECC_UNCORRECTABLE
 *   GPU-busy      context: CUDA_ERROR_DEVICES_UNAVAILABLE (exclusive mode)
 *   GPU-oom       alloc: CUDA_ERROR_OUT_OF_MEMORY
 *   GPU-mismatch  the buffer reads back other bytes
 *   GPU-hang      cuInit never returns
 */
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef int CUresult;
typedef int CUdevice;
typedef void *CUcontext;
typedef unsigned long long CUdeviceptr;

static unsigned char mem[1 << 20];
static unsigned char filled;

static int is(const char *gpu)
{
    const char *v = getenv("CUDA_VISIBLE_DEVICES");
    return v && strcmp(v, gpu) == 0;
}

CUresult cuGetErrorName(CUresult r, const char **s)
{
    switch (r) {
    case 2: *s = "CUDA_ERROR_OUT_OF_MEMORY"; return 0;
    case 46: *s = "CUDA_ERROR_DEVICES_UNAVAILABLE"; return 0;
    case 100: *s = "CUDA_ERROR_NO_DEVICE"; return 0;
    case 214: *s = "CUDA_ERROR_ECC_UNCORRECTABLE"; return 0;
    case 999: *s = "CUDA_ERROR_UNKNOWN"; return 0;
    }
    return 1;
}

CUresult cuInit(unsigned flags)
{
    (void)flags;
    if (is("GPU-hang"))
        for (;;) pause();
    return is("GPU-init") ? 999 : 0;
}

CUresult cuDeviceGetCount(int *n)
{
    *n = is("GPU-none") ? 0 : is("GPU-two") ? 2 : 1;
    return 0;
}

CUresult cuDeviceGet(CUdevice *d, int i) { *d = i; return 0; }

CUresult cuDevicePrimaryCtxRetain(CUcontext *c, CUdevice d)
{
    (void)d;
    *c = mem;
    return is("GPU-ecc") ? 214 : is("GPU-busy") ? 46 : 0;
}

CUresult cuDevicePrimaryCtxRelease_v2(CUdevice d) { (void)d; return 0; }
CUresult cuCtxSetCurrent(CUcontext c) { (void)c; return 0; }

CUresult cuMemAlloc_v2(CUdeviceptr *p, size_t n)
{
    (void)n;
    *p = 1;
    return is("GPU-oom") ? 2 : 0;
}

CUresult cuMemsetD8_v2(CUdeviceptr p, unsigned char v, size_t n)
{
    (void)p; (void)n;
    filled = v;
    return 0;
}

CUresult cuMemcpyDtoH_v2(void *dst, CUdeviceptr p, size_t n)
{
    (void)p;
    memset(dst, is("GPU-mismatch") ? 0 : filled, n);
    return 0;
}

CUresult cuCtxSynchronize(void) { return 0; }
CUresult cuMemFree_v2(CUdeviceptr p) { (void)p; return 0; }

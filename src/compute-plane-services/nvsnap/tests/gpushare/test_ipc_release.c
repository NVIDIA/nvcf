/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * Probe: legacy CUDA IPC (cuIpcGetMemHandle / cuIpcOpenMemHandle, as used
 * by vLLM custom all-reduce) across cuda-checkpoint.
 *
 * E allocates with cuMemAlloc and exports an IPC handle; I opens it. I
 * closes the handle before checkpoint, both are checkpointed + restored,
 * then I re-opens it. Pointers held by the app are only valid if the
 * re-open lands at the same VA — the driver picks it, so we measure.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_ipc_release \
 *            test_ipc_release.c -lcuda
 * Run:   ./test_ipc_release <nvsnap-gpu-suspend> [de=0] [di=1]
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

struct chan { int cmd[2], rep[2]; };
static void put(int fd, const void *p, size_t n) { if (write(fd, p, n) != (ssize_t)n) exit(2); }
static void get(int fd, void *p, size_t n) { if (read(fd, p, n) != (ssize_t)n) exit(2); }

/* Reply is a uint64 (VA or check result). */
static uint64_t call(struct chan *c, char op, uint32_t arg)
{
    uint64_t r;
    put(c->cmd[1], &op, 1); put(c->cmd[1], &arg, 4); get(c->rep[0], &r, 8);
    return r;
}

static uint64_t check(CUdeviceptr va, uint32_t want)
{
    uint32_t *h = malloc(SIZE);
    uint64_t bad = 0;
    CK(cuMemcpyDtoH(h, va, SIZE));
    for (size_t i = 0; i < SIZE / 4; i++)
        if (h[i] != want) { bad = 1; break; }
    free(h);
    return bad;
}

static void ctx_on(int dev)
{
    CUdevice d; CUcontext ctx;
    CK(cuInit(0)); CK(cuDeviceGet(&d, dev)); CK(cuDevicePrimaryCtxRetain(&ctx, d)); CK(cuCtxSetCurrent(ctx));
}

/* E: 'h' send IPC handle on hp, 'w' write, 'c' check.
 * I: 'o' open handle from hp, 'x' close, 'c' check, every reply = VA for 'o'. */
static void child(struct chan *c, int hp[2], int dev, int is_exporter)
{
    CUdeviceptr va = 0;
    CUipcMemHandle ih;
    ctx_on(dev);
    if (is_exporter) CK(cuMemAlloc(&va, SIZE));
    for (;;) {
        char op; uint32_t arg; uint64_t r = 0;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'h': CK(cuIpcGetMemHandle(&ih, va)); put(hp[1], &ih, sizeof ih); break;
        case 'o':
            get(hp[0], &ih, sizeof ih);
            r = cuIpcOpenMemHandle(&va, ih, CU_IPC_MEM_LAZY_ENABLE_PEER_ACCESS);
            r = r ? (1UL << 63) | r : va; break;
        case 'n': CK(cuMemAlloc(&va, SIZE)); CK(cuMemsetD32(va, arg, SIZE / 4)); CK(cuCtxSynchronize()); break;
        case 'x': CK(cuIpcCloseMemHandle(va)); break;
        case 'w': CK(cuMemsetD32(va, arg, SIZE / 4)); CK(cuCtxSynchronize()); break;
        case 'c': r = check(va, arg); break;
        case 'q': put(c->rep[1], &r, 8); exit(0);
        }
        put(c->rep[1], &r, 8);
    }
}

static const char *tool;
static int run(const char *action, pid_t a, pid_t b)
{
    char cmd[512];
    snprintf(cmd, sizeof cmd, "%s %s %d %d", tool, action, a, b);
    printf("$ %s\n", cmd); fflush(stdout);
    int st = system(cmd);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 1, fails = 0;
    struct chan ce, ci;
    int hp[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || pipe(hp)) return 1;
    pid_t pe = fork();
    if (!pe) child(&ce, hp, de, 1);
    pid_t pi = fork();
    if (!pi) child(&ci, hp, di, 0);
    printf("exporter pid %d (dev %d), importer pid %d (dev %d)\n", pe, de, pi, di);

    call(&ce, 'w', 0x11111111);
    call(&ce, 'h', 0);
    uint64_t va0 = call(&ci, 'o', 0);
    EXPECT(call(&ci, 'c', 0x11111111) == 0, "importer sees exporter's data");

    /* Does a plain close/re-open (no checkpoint) keep the VA? */
    call(&ci, 'x', 0); call(&ce, 'h', 0);
    uint64_t va1 = call(&ci, 'o', 0);
    printf("  re-open without checkpoint: VA %#lx -> %#lx (%s)\n", va0, va1, va0 == va1 ? "same" : "DIFFERENT");

    call(&ci, 'x', 0);
    EXPECT(run("full-save", pe, pi) == 0, "full-save E I after close");
    EXPECT(run("full-restore", pe, pi) == 0, "full-restore E I");
    EXPECT(call(&ce, 'c', 0x11111111) == 0, "exporter data intact after restore");
    call(&ce, 'h', 0);
    uint64_t va2 = call(&ci, 'o', 0);
    printf("  re-open after restore: VA %#lx -> %#lx\n", va1, va2);
    if (va2 >> 63) {
        printf("  re-open of the restored allocation failed: CUresult %lu\n", va2 & 0xffff);
        call(&ce, 'n', 0x33333333); call(&ce, 'h', 0);
        uint64_t va3 = call(&ci, 'o', 0);
        printf("  open of a NEW allocation made after restore: %s (%#lx)\n", va3 >> 63 ? "FAILS" : "works", va3);
        kill(pe, SIGKILL); kill(pi, SIGKILL);
        return 1;
    }
    EXPECT(va2 == va1, "re-opened at the same VA");
    EXPECT(call(&ci, 'c', 0x11111111) == 0, "importer sees data after re-open");
    call(&ce, 'w', 0x22222222);
    EXPECT(call(&ci, 'c', 0x22222222) == 0, "sharing live: exporter write visible to importer");

    call(&ce, 'q', 0); call(&ci, 'q', 0);
    waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}

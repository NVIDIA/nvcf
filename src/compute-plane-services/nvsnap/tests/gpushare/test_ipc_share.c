/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * CUDA IPC (cuIpcGetMemHandle / cuIpcOpenMemHandle, as used by vLLM custom
 * all-reduce) across nvsnap-gpu-suspend suspend/resume, with
 * LD_PRELOAD=libnvsnap_gpushare.so.
 *
 * E allocates with cuMemAlloc, fills it and passes an IPC handle to I,
 * which opens it. suspend + resume both. Afterwards I must see E's data
 * at the same pointer, and sharing must be live (E writes, I reads).
 * Finally I closes the handle and E frees the memory.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_ipc_share \
 *            test_ipc_share.c -lcuda
 * Run:   LD_PRELOAD=libnvsnap_gpushare.so ./test_ipc_share <nvsnap-gpu-suspend> [de=0] [di=1] [cycles=2]
 */
#include <cuda.h>
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

/* E: 'h' send IPC handle, 'w' write, 'c' check, 'f' free.
 * I: 'o' open (reply = VA), 'c' check, 'x' close. */
static void child(struct chan *c, int hp[2], int dev, int is_exporter)
{
    CUdevice d; CUcontext ctx; CUdeviceptr va = 0; CUipcMemHandle ih;
    CK(cuInit(0)); CK(cuDeviceGet(&d, dev)); CK(cuDevicePrimaryCtxRetain(&ctx, d)); CK(cuCtxSetCurrent(ctx));
    if (is_exporter) CK(cuMemAlloc(&va, SIZE));
    for (;;) {
        char op; uint32_t arg; uint64_t r = 0;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'h': CK(cuIpcGetMemHandle(&ih, va)); put(hp[1], &ih, sizeof ih); break;
        case 'o':
            get(hp[0], &ih, sizeof ih);
            CK(cuIpcOpenMemHandle(&va, ih, CU_IPC_MEM_LAZY_ENABLE_PEER_ACCESS));
            r = va; break;
        case 'x': CK(cuIpcCloseMemHandle(va)); break;
        case 'f': CK(cuMemFree(va)); break;
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
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di] [cycles]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 1;
    int cycles = argc > 4 ? atoi(argv[4]) : 2, fails = 0;
    struct chan ce, ci;
    int hp[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || pipe(hp)) return 1;
    pid_t pe = fork();
    if (!pe) child(&ce, hp, de, 1);
    pid_t pi = fork();
    if (!pi) child(&ci, hp, di, 0);
    printf("exporter pid %d (dev %d), importer pid %d (dev %d)\n", pe, de, pi, di);

    uint32_t v = 0x11111111;
    call(&ce, 'w', v);
    call(&ce, 'h', 0);
    uint64_t va0 = call(&ci, 'o', 0);
    EXPECT(call(&ci, 'c', v) == 0, "importer sees exporter's data");

    for (int c = 1; c <= cycles && !fails; c++) {
        printf("--- cycle %d/%d\n", c, cycles);
        EXPECT(run("suspend", pe, pi) == 0, "suspend E I");
        EXPECT(run("resume", pe, pi) == 0, "resume E I");
        EXPECT(call(&ce, 'c', v) == 0, "exporter data intact");
        EXPECT(call(&ci, 'c', v) == 0, "importer sees the data at the same pointer");
        v += 0x11111111;
        call(&ce, 'w', v);
        EXPECT(call(&ci, 'c', v) == 0, "sharing live: exporter write visible to importer");
    }
    /* The same allocation re-shared after resume opens at a new pointer. */
    call(&ce, 'h', 0);
    uint64_t va1 = call(&ci, 'o', 0);
    EXPECT(va1 && call(&ci, 'c', v) == 0, "handle taken after resume opens and sees the data");
    printf("  first open %#lx, second open %#lx\n", va0, va1);
    call(&ci, 'x', 0);
    call(&ce, 'f', 0);

    call(&ce, 'q', 0); call(&ci, 'q', 0);
    waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * A CUDA IPC handle of small cuMemAlloc memory (below the shim's cuMem
 * threshold, so legacy memory), taken and never opened, as vLLM's
 * multi-node custom all-reduce does with its signal buffer. A legacy IPC
 * export makes the driver's checkpoint (610) crash the process, so the
 * shim exports lazily: only when a peer opens the handle.
 *
 * E allocates, fills it and takes an IPC handle; suspend + resume E. E's
 * data must be intact. Then I opens the handle (the export is made now)
 * and must see E's data, and E's writes.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_ipc_legacy \
 *            test_ipc_legacy.c -lcuda
 * Run:   LD_PRELOAD=libnvsnap_gpushare.so ./test_ipc_legacy <nvsnap-gpu-suspend> [de=0] [di=1] [cycles=2]
 */
#include <cuda.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE 4864UL  /* vLLM custom all-reduce's meta buffer */
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
    uint32_t h[SIZE / 4];
    CK(cuMemcpyDtoH(h, va, SIZE));
    for (size_t i = 0; i < SIZE / 4; i++)
        if (h[i] != want) return 1;
    return 0;
}

/* E: 'h' take an IPC handle (kept until 's'), 's' send it, 'w' write,
 * 'c' check, 'f' free. I: 'o' open (reply = VA), 'c' check, 'x' close. */
static void child(struct chan *c, int hp[2], int dev, int is_exporter)
{
    CUdevice d; CUcontext ctx; CUdeviceptr va = 0; CUipcMemHandle ih;
    CK(cuInit(0)); CK(cuDeviceGet(&d, dev)); CK(cuDevicePrimaryCtxRetain(&ctx, d)); CK(cuCtxSetCurrent(ctx));
    if (is_exporter) CK(cuMemAlloc(&va, SIZE));
    for (;;) {
        char op; uint32_t arg; uint64_t r = 0;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'h': CK(cuIpcGetMemHandle(&ih, va)); break;
        case 's': put(hp[1], &ih, sizeof ih); break;
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
static int run(const char *action, pid_t a)
{
    char cmd[512];
    snprintf(cmd, sizeof cmd, "%s %s %d", tool, action, a);
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
    printf("exporter pid %d (dev %d)\n", pe, de);

    uint32_t v = 0x11111111;
    call(&ce, 'w', v);
    call(&ce, 'h', 0);  /* taken, not opened by anyone yet */
    for (int c = 1; c <= cycles && !fails; c++) {
        printf("--- cycle %d/%d\n", c, cycles);
        EXPECT(run("suspend", pe) == 0, "suspend E (holding an unopened IPC handle)");
        EXPECT(run("resume", pe) == 0, "resume E");
        EXPECT(call(&ce, 'c', v) == 0, "exporter data intact");
    }

    pid_t pi = fork();
    if (!pi) child(&ci, hp, di, 0);
    printf("importer pid %d (dev %d)\n", pi, di);
    call(&ce, 's', 0);
    uint64_t va = call(&ci, 'o', 0);
    EXPECT(va && call(&ci, 'c', v) == 0, "handle taken before the checkpoint opens and sees the data");
    v += 0x11111111;
    call(&ce, 'w', v);
    EXPECT(call(&ci, 'c', v) == 0, "sharing live: exporter write visible to importer");
    call(&ci, 'x', 0);
    call(&ce, 'f', 0);

    call(&ce, 'q', 0); call(&ci, 'q', 0);
    waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}

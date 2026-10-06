/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * A suspend that fails must leave the workload running as before.
 *
 * E allocates GPU memory and shares it through CUDA IPC; I maps it, holds
 * its own allocation and two page-locked host buffers (one registered, one
 * from cuMemHostAlloc). nvsnap-gpu-suspend suspend then fails at three
 * points, and each time the tool must report a rollback that worked, and E
 * and I must work as before (contents, the IPC mapping, registered host
 * memory) and checkpoint normally afterwards:
 *
 *   1. before anything is released: the checkpoint directory cannot be
 *      created;
 *   2. partway through "release": imports are released, then saving an
 *      allocation fails (the store's chunks directory is a file), with the
 *      host buffers still registered;
 *   3. after a complete "release": the driver lock fails (a third process,
 *      X, is already checkpointed).
 *
 * Then a normal suspend and resume, with a second shared allocation that E
 * has freed while I still maps it (it must stay reserved and unmapped), and
 * a resume that fails after the driver restore (the chunk store is gone)
 * and succeeds when run again once the store is back.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_release_rollback test_release_rollback.c \
 *            -L/usr/local/cuda/lib64/stubs -lcuda
 * Run:   LD_PRELOAD=.../libnvsnap_gpushare.so ./test_release_rollback <nvsnap-gpu-suspend> <scratch-dir> [de=0] [di=1]
 */
#include <cuda.h>
#include <fcntl.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE (64UL << 20)
#define HOST (2UL << 20)
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

struct chan { int cmd[2], rep[2]; };
static void put(int fd, const void *p, size_t n) { if (write(fd, p, n) != (ssize_t)n) exit(2); }
static void get(int fd, void *p, size_t n) { if (read(fd, p, n) != (ssize_t)n) exit(2); }

static uint64_t call(struct chan *c, char op, uint32_t arg)
{
    uint64_t r = 1;
    put(c->cmd[1], &op, 1);
    put(c->cmd[1], &arg, 4);
    get(c->rep[0], &r, 8);
    return r;
}

/* 0 if every word of [va, va+SIZE) is want. */
static uint64_t check(CUdeviceptr va, uint32_t want)
{
    uint32_t *h = malloc(SIZE);
    uint64_t bad = 0;
    CK(cuMemcpyDtoH(h, va, SIZE));
    for (size_t i = 0; i < SIZE / 4 && !bad; i++) bad = h[i] != want;
    free(h);
    return bad;
}

/* Page-locked: the driver has a device pointer for it, and the GPU writes it. */
static uint64_t check_host(void *p, uint32_t v)
{
    CUdeviceptr d;
    if (cuMemHostGetDevicePointer(&d, p, 0) != CUDA_SUCCESS) return 1;
    CK(cuMemsetD32(d, v, HOST / 4));
    CK(cuCtxSynchronize());
    for (size_t i = 0; i < HOST / 4; i++) if (((uint32_t *)p)[i] != v) return 1;
    return 0;
}

/* E: 'h' send IPC handle, 'w' write, 'c' check own buffer, 'H' allocate a
 * second buffer and send its handle, 'F' free it.
 * I: 'o' open the handle, 'O' open the second one, 'c' check the import
 * (arg) and own memory.
 * X: nothing to do. 'q' quits. */
static void child(struct chan *c, int hp[2], int dev, char role)
{
    CUdevice d;
    CUcontext ctx;
    CUdeviceptr va = 0, own = 0, va2 = 0;
    CUipcMemHandle ih;
    void *reg = NULL, *alloc = NULL;
    CK(cuInit(0));
    CK(cuDeviceGet(&d, dev));
    CK(cuDevicePrimaryCtxRetain(&ctx, d));
    CK(cuCtxSetCurrent(ctx));
    if (role == 'E') {
        CK(cuMemAlloc(&va, SIZE));
        CK(cuMemsetD32(va, 0x11111111, SIZE / 4));
    } else if (role == 'I') {
        CK(cuMemAlloc(&own, SIZE));
        CK(cuMemsetD32(own, 0x22222222, SIZE / 4));
        if (posix_memalign(&reg, 4096, HOST)) exit(2);
        CK(cuMemHostRegister(reg, HOST, CU_MEMHOSTREGISTER_DEVICEMAP));
        CK(cuMemHostAlloc(&alloc, HOST, CU_MEMHOSTALLOC_DEVICEMAP));
    } else {
        CK(cuMemAlloc(&va, 1 << 20));  /* below the shim's 2 MiB: no control socket */
    }
    CK(cuCtxSynchronize());
    for (;;) {
        char op;
        uint32_t arg;
        uint64_t r = 0;
        get(c->cmd[0], &op, 1);
        get(c->cmd[0], &arg, 4);
        switch (op) {
        case 'h': CK(cuIpcGetMemHandle(&ih, va)); put(hp[1], &ih, sizeof ih); break;
        case 'H':
            CK(cuMemAlloc(&va2, SIZE));
            CK(cuIpcGetMemHandle(&ih, va2));
            put(hp[1], &ih, sizeof ih);
            break;
        case 'F': CK(cuMemFree(va2)); break;
        case 'O':
            get(hp[0], &ih, sizeof ih);
            r = cuIpcOpenMemHandle(&va2, ih, CU_IPC_MEM_LAZY_ENABLE_PEER_ACCESS);
            break;
        case 'o':
            get(hp[0], &ih, sizeof ih);
            r = cuIpcOpenMemHandle(&va, ih, CU_IPC_MEM_LAZY_ENABLE_PEER_ACCESS);
            break;
        case 'w': CK(cuMemsetD32(va, arg, SIZE / 4)); CK(cuCtxSynchronize()); break;
        case 'c':
            r = check(va, arg);
            if (role == 'I') r = r || check(own, 0x22222222) || check_host(reg, arg) || check_host(alloc, ~arg);
            break;
        case 'q': put(c->rep[1], &r, 8); exit(0);
        }
        put(c->rep[1], &r, 8);
    }
}

static const char *tool;
static pid_t pe, pi, px;

/* Run the tool; returns its exit status, and whether it reported a rollback
 * that worked (ok) or failed (bad). */
static int run(const char *args, const char *pids, int *ok, int *bad)
{
    char cmd[2048], line[512];
    snprintf(cmd, sizeof cmd, "%s %s %s 2>&1", tool, args, pids);
    printf("$ %s\n", cmd);
    FILE *f = popen(cmd, "r");
    if (!f) return -1;
    *ok = *bad = 0;
    while (fgets(line, sizeof line, f)) {
        if (strstr(line, "rolled back, the workload runs as before")) *ok = 1;
        if (strstr(line, "so did the rollback")) *bad = 1;
        if (strstr(line, "err") || strstr(line, "rolled back") || strstr(line, "rollback")) printf("    %s", line);
    }
    int st = pclose(f);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

static int fails;
#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); \
    if (!ok_) fails++; } while (0)

static struct chan ce, ci, cx;
static uint32_t gen = 0x33333333;

/* E and I work as before: contents, the live IPC mapping, host memory. */
static void works(const char *when)
{
    char msg[256];
    snprintf(msg, sizeof msg, "%s: E's data seen through I's import, I's memory intact", when);
    EXPECT(call(&ce, 'c', gen) == 0 && call(&ci, 'c', gen) == 0, msg);
    gen += 0x01010101;
    call(&ce, 'w', gen);
    snprintf(msg, sizeof msg, "%s: sharing live (E writes, I reads)", when);
    EXPECT(call(&ci, 'c', gen) == 0, msg);
}

int main(int argc, char **argv)
{
    if (argc < 3) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> <scratch-dir> [de] [di]\n", argv[0]); return 1; }
    tool = argv[1];
    const char *dir = argv[2];
    int de = argc > 3 ? atoi(argv[3]) : 0, di = argc > 4 ? atoi(argv[4]) : 1, ok, bad;
    int hp[2];
    char path[600], args[1400], pids[64], all[96];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || pipe(cx.cmd) || pipe(cx.rep) || pipe(hp))
        return 1;
    if ((pe = fork()) == 0) child(&ce, hp, de, 'E');
    if ((pi = fork()) == 0) child(&ci, hp, di, 'I');
    if ((px = fork()) == 0) child(&cx, hp, de, 'X');
    if (pe < 0 || pi < 0 || px < 0) { perror("fork"); goto out; }
    snprintf(pids, sizeof pids, "%d %d", pe, pi);
    snprintf(all, sizeof all, "%d %d %d", pe, pi, px);
    printf("E pid %d (dev %d), I pid %d (dev %d), X pid %d\n", pe, de, pi, di, px);
    mkdir(dir, 0755);

    call(&ce, 'w', gen);
    call(&ce, 'h', 0);
    EXPECT(call(&ci, 'o', 0) == 0, "I opens E's IPC handle");
    works("before");

    printf("--- 1. release fails before releasing anything\n");
    snprintf(args, sizeof args, "--store %s/s1 --ckpt-dir %s/missing/ckpt suspend", dir, dir);
    EXPECT(run(args, pids, &ok, &bad) != 0 && ok && !bad, "suspend fails, rollback reported ok");
    works("after 1");

    printf("--- 2. release fails partway: an allocation cannot be saved\n");
    snprintf(path, sizeof path, "%s/s2", dir);
    mkdir(path, 0755);
    snprintf(path, sizeof path, "%s/s2/chunks", dir);
    close(open(path, O_CREAT | O_WRONLY, 0644));  /* a file: chunk writes fail */
    snprintf(args, sizeof args, "--store %s/s2 --ckpt-dir %s/c2 suspend", dir, dir);
    EXPECT(run(args, pids, &ok, &bad) != 0 && ok && !bad, "suspend fails, rollback reported ok");
    works("after 2");

    printf("--- 3. the driver lock fails after a complete release\n");
    snprintf(args, sizeof args, "full-save %d", px);
    EXPECT(run(args, "", &ok, &bad) == 0, "X checkpointed beforehand");
    snprintf(args, sizeof args, "--store %s/s3 --ckpt-dir %s/c3 suspend", dir, dir);
    EXPECT(run(args, all, &ok, &bad) != 0 && ok && !bad, "suspend fails, rollback reported ok");
    works("after 3");

    printf("--- 4. a normal checkpoint still works, with an import its exporter freed\n");
    call(&ce, 'H', 0);
    EXPECT(call(&ci, 'O', 0) == 0, "I opens E's second buffer");
    call(&ce, 'F', 0);
    snprintf(args, sizeof args, "--store %s/s4 --ckpt-dir %s/c4 suspend", dir, dir);
    EXPECT(run(args, pids, &ok, &bad) == 0, "suspend");
    EXPECT(run("resume", pids, &ok, &bad) == 0, "resume");
    works("after 4");

    printf("--- 5. resume fails after the driver restore, then a retry completes\n");
    char st[700], moved[700];
    snprintf(st, sizeof st, "%s/s5", dir);
    snprintf(moved, sizeof moved, "%s/s5.away", dir);
    snprintf(args, sizeof args, "--store %s --ckpt-dir %s/c5 suspend", st, dir);
    EXPECT(run(args, pids, &ok, &bad) == 0, "suspend to a chunk store");
    EXPECT(rename(st, moved) == 0, "chunk store moved away");
    EXPECT(run("resume", pids, &ok, &bad) != 0, "resume fails (chunks missing)");
    EXPECT(rename(moved, st) == 0, "chunk store back");
    EXPECT(run("resume", pids, &ok, &bad) == 0, "resume again completes");
    works("after 5");

out:
    for (int i = 0; i < 3; i++) {
        pid_t p = i == 0 ? pe : i == 1 ? pi : px;
        if (p > 0) kill(p, SIGKILL);
        if (p > 0) waitpid(p, NULL, 0);
    }
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}

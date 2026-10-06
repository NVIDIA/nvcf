/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * One process driving two GPUs, under libnvsnap_gpushare.
 *
 * The shim backs cuMemAlloc with cuMem memory, which cuCtxEnablePeerAccess
 * does not open to peers by itself, and saves and re-creates that memory
 * around a checkpoint. The child allocates a buffer on each GPU, enables
 * peer access both ways, and has a kernel on each GPU write into the
 * other's buffer. Then nvsnap-gpu-suspend suspend + resume runs on it,
 * keeping the saved memory in host RAM, then again with a chunk store.
 * Checks: peer writes work before and after, on buffers allocated before
 * and after the restore and after peer access is disabled and enabled
 * again; the contents of both buffers survive.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_multi_gpu test_multi_gpu.c \
 *            -L/usr/local/cuda/lib64/stubs -lcuda
 * Run:   LD_PRELOAD=.../libnvsnap_gpushare.so ./test_multi_gpu <nvsnap-gpu-suspend> <scratch-dir>
 *        (needs 2 GPUs with peer access; scratch-dir holds the chunk store)
 */
#include <cuda.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
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

static CUcontext ctx[2];
static CUfunction fill[2];
static CUdeviceptr buf[3];  /* on GPU 0, GPU 1, and GPU 0 (allocated after restore) */
static const uint32_t pat[3] = { 0xA0A0A0A0, 0xB1B1B1B1, 0xC0C0C0C0 };

/* A kernel on GPU g stores v at buf[i][0] (on the other GPU). */
static void peer_write(int g, int i, uint32_t v)
{
    void *args[] = { &buf[i], &v };
    CK(cuCtxSetCurrent(ctx[g]));
    CK(cuLaunchKernel(fill[g], 1, 1, 1, 1, 1, 1, 0, 0, args, NULL));
    CK(cuCtxSynchronize());
}

/* buf[i] holds v at 0 and its pattern elsewhere (read on its own GPU). */
static int check(int i, uint32_t v)
{
    uint32_t *h = malloc(SIZE);
    CK(cuCtxSetCurrent(ctx[i == 1]));
    CK(cuMemcpyDtoH(h, buf[i], SIZE));
    int bad = h[0] != v;
    for (size_t k = 1; !bad && k < SIZE / 4; k++) bad = h[k] != pat[i];
    if (bad) fprintf(stderr, "buf %d: [0]=%#x (want %#x) [1]=%#x\n", i, h[0], v, h[1]);
    free(h);
    return bad;
}

static void alloc(int i)
{
    CK(cuCtxSetCurrent(ctx[i == 1]));
    CK(cuMemAlloc(&buf[i], SIZE));
    CK(cuMemsetD32(buf[i], pat[i], SIZE / 4));
    CK(cuCtxSynchronize());
}

/* Commands on cmd, one reply byte (0 = ok) on rep:
 * 's' set up (2: the GPUs cannot access each other), 'w' peer writes + check, 'n' new buffer on GPU 0 + peer write,
 * 'd' disable and re-enable peer access, then peer writes, 'q' quit. */
static void child(int cmd, int rep)
{
    CUdevice d[2];
    CUmodule m;
    uint32_t gen = 0;
    char op;
    while (read(cmd, &op, 1) == 1) {
        char r = 0;
        switch (op) {
        case 's':
            CK(cuInit(0));
            for (int g = 0; g < 2; g++) CK(cuDeviceGet(&d[g], g));
            int p01 = 0, p10 = 0;
            CK(cuDeviceCanAccessPeer(&p01, d[0], d[1]));
            CK(cuDeviceCanAccessPeer(&p10, d[1], d[0]));
            if (!p01 || !p10) { r = 2; break; }
            for (int g = 0; g < 2; g++) {
                CK(cuDevicePrimaryCtxRetain(&ctx[g], d[g]));
                CK(cuCtxSetCurrent(ctx[g]));
                CK(cuModuleLoadData(&m, ptx));
                CK(cuModuleGetFunction(&fill[g], m, "fill"));
            }
            alloc(0);
            alloc(1);
            for (int g = 0; g < 2; g++) {
                CK(cuCtxSetCurrent(ctx[g]));
                CK(cuCtxEnablePeerAccess(ctx[!g], 0));
            }
            break;
        case 'w':
            gen++;
            peer_write(1, 0, gen);  /* GPU 1 writes GPU 0's buffer */
            peer_write(0, 1, gen);  /* and back */
            r = check(0, gen) || check(1, gen);
            break;
        case 'n':
            alloc(2);
            peer_write(1, 2, 0x5eed);
            r = check(2, 0x5eed);
            break;
        case 'd':
            CK(cuCtxSetCurrent(ctx[1]));
            CK(cuCtxDisablePeerAccess(ctx[0]));
            CK(cuCtxEnablePeerAccess(ctx[0], 0));
            gen++;
            peer_write(1, 0, gen);
            r = check(0, gen);
            break;
        case 'q':
            exit(0);
        }
        if (write(rep, &r, 1) != 1) exit(2);
    }
    exit(2);
}

static int cmd[2], rep[2];
static pid_t pid;

/* Send op; 0 if the child replied ok. */
static int call(char op)
{
    char r = 1;
    if (write(cmd[1], &op, 1) != 1 || read(rep[0], &r, 1) != 1) return -1;
    return r;
}

static const char *tool;
static int run(const char *args)
{
    char c[1024];
    snprintf(c, sizeof c, "%s %s %d", tool, args, pid);
    printf("$ %s\n", c);
    int st = system(c);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

static int fails;
#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); \
    if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 3) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> <scratch-dir>\n", argv[0]); return 1; }
    tool = argv[1];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(cmd) || pipe(rep)) return 1;
    if ((pid = fork()) < 0) { perror("fork"); return 1; }
    if (!pid) { close(cmd[1]); close(rep[0]); child(cmd[0], rep[1]); }
    close(cmd[0]);
    close(rep[1]);
    printf("pid %d, GPUs 0 and 1\n", pid);

    int r = call('s');
    if (r == 2) {
        printf("=== SKIP: GPUs 0 and 1 have no peer access\n");
        kill(pid, SIGKILL);
        waitpid(pid, NULL, 0);
        return 0;
    }
    EXPECT(r == 0, "buffers on both GPUs, peer access enabled both ways");
    EXPECT(call('w') == 0, "peer writes before checkpoint");

    char store[600], ckpt[600], args[1400];
    snprintf(store, sizeof store, "%s/store", argv[2]);
    snprintf(ckpt, sizeof ckpt, "%s/ckpt", argv[2]);
    mkdir(argv[2], 0755);
    mkdir(ckpt, 0755);
    /* Stop at the first failure: the child may be left stopped. */
    for (int pass = 0; pass < 2 && !fails; pass++) {
        printf("--- %s\n", pass ? "chunk store" : "host memory");
        snprintf(args, sizeof args, pass ? "--store %s --ckpt-dir %s suspend" : "suspend", store, ckpt);
        EXPECT(run(args) == 0, "suspend");
        if (!fails) EXPECT(run("resume") == 0, "resume");
        if (!fails) EXPECT(call('w') == 0, "both buffers intact, peer writes work after restore");
    }
    if (!fails) EXPECT(call('n') == 0, "peer write to a buffer allocated after restore");
    if (!fails) EXPECT(call('d') == 0, "peer access disabled and enabled again");
    if (!fails) EXPECT(run("suspend") == 0 && run("resume") == 0, "suspend + resume again");
    if (!fails) EXPECT(call('w') == 0, "peer writes after the last restore");

    if (fails || call('q') < 0) kill(pid, SIGKILL);
    int st = 0;
    waitpid(pid, &st, 0);
    if (!fails) EXPECT(WIFEXITED(st) && WEXITSTATUS(st) == 0, "child exited cleanly");
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}

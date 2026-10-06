/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * Multi-rank checkpoint/restore test for nvsnap-gpu-suspend.
 *
 * Forks N ranks (rank r uses GPU r % device_count). Each rank keeps a live
 * NCCL communicator and loops forever doing:
 *   - allreduce(rank+1) and checks the sum == N*(N+1)/2
 *   - D2H of a persistent buffer written once at startup, checks the pattern
 *
 * While the ranks are running, the parent runs (for each cycle):
 *   nvsnap-gpu-suspend full-save    <pids...>   → all CHECKPOINTED
 *   (verify no rank makes progress)
 *   nvsnap-gpu-suspend full-restore <pids...>   → all RUNNING
 *   (verify every rank makes progress again)
 *
 * Any wrong allreduce result or corrupted persistent buffer fails the test.
 * Without NCCL (or with fewer GPUs than ranks — NCCL rejects two ranks on
 * one GPU) the ranks run only the persistent-buffer check, which still
 * exercises the multi-process lock/checkpoint/restore/unlock path.
 *
 * Build:
 *   gcc -O2 -o test_checkpoint_nccl test_checkpoint_nccl.c -ldl
 *
 * Run (two GPUs, NCCL available):
 *   ./test_checkpoint_nccl build/lib/nvsnap-gpu-suspend 2 256 3
 *   args: <tool> [nranks=2] [persist_mb=256] [cycles=3] [suspend]
 * With "suspend", cycles use suspend/resume instead of full-save/
 * full-restore. That is required when ranks share GPU memory (NCCL cuMem
 * P2P, the NCCL default) — run with LD_PRELOAD=libnvsnap_gpushare.so.
 */
#include <dlfcn.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

typedef int cudaError_t;
typedef void* ncclComm_t;
typedef void* cudaStream_t;

/* NCCL types */
typedef struct { char internal[128]; } ncclUniqueId;
typedef int ncclResult_t;
#define ncclSuccess 0
#define ncclFloat 7
#define ncclSum 0

#define cudaMemcpyHostToDevice 1
#define cudaMemcpyDeviceToHost 2

#define MAX_RANKS 16
#define AR_COUNT (1024 * 1024)

/* Function pointer types */
typedef cudaError_t (*cudaGetDeviceCount_t)(int*);
typedef cudaError_t (*cudaSetDevice_t)(int);
typedef cudaError_t (*cudaMalloc_t)(void**, size_t);
typedef cudaError_t (*cudaMemcpy_t)(void*, const void*, size_t, int);
typedef cudaError_t (*cudaDeviceSynchronize_t)(void);

typedef ncclResult_t (*ncclGetUniqueId_t)(ncclUniqueId*);
typedef ncclResult_t (*ncclCommInitRank_t)(ncclComm_t*, int, ncclUniqueId, int);
typedef ncclResult_t (*ncclAllReduce_t)(const void*, void*, size_t, int, int, ncclComm_t, cudaStream_t);

/* Shared between parent and ranks (MAP_SHARED). */
struct shared {
    volatile int stop;
    volatile int id_ready;
    ncclUniqueId nccl_id;
    volatile int ready[MAX_RANKS];
    volatile long iters[MAX_RANKS];
    volatile long errors[MAX_RANKS];
};

static int nranks = 2;
static size_t persist_bytes = 256u << 20;
static int use_nccl;

static void *load_lib(const char *name)
{
    void *h = dlopen(name, RTLD_LAZY);
    if (!h) fprintf(stderr, "Cannot load %s: %s\n", name, dlerror());
    return h;
}

static uint32_t pattern(int rank, size_t i)
{
    return (uint32_t)(i * 2654435761u) ^ (uint32_t)(rank * 0x9e3779b9u);
}

/* ── Rank process ─────────────────────────────────────────────────────── */

static int run_rank(int rank, int device, struct shared *sh)
{
    void *cudart = load_lib("libcudart.so");
    if (!cudart) return 1;
    cudaSetDevice_t cudaSetDevice = dlsym(cudart, "cudaSetDevice");
    cudaMalloc_t cudaMalloc = dlsym(cudart, "cudaMalloc");
    cudaMemcpy_t cudaMemcpy = dlsym(cudart, "cudaMemcpy");
    cudaDeviceSynchronize_t cudaSync = dlsym(cudart, "cudaDeviceSynchronize");

    ncclAllReduce_t ncclAR = NULL;
    ncclComm_t comm = NULL;

    if (cudaSetDevice(device) != 0) { fprintf(stderr, "[rank %d] cudaSetDevice failed\n", rank); return 1; }

    /* Persistent buffer: written once, verified every iteration. */
    size_t n = persist_bytes / sizeof(uint32_t);
    uint32_t *host = malloc(persist_bytes);
    uint32_t *check = malloc(persist_bytes);
    void *persist = NULL;
    for (size_t i = 0; i < n; i++) host[i] = pattern(rank, i);
    if (cudaMalloc(&persist, persist_bytes) != 0 ||
        cudaMemcpy(persist, host, persist_bytes, cudaMemcpyHostToDevice) != 0) {
        fprintf(stderr, "[rank %d] persistent buffer setup failed\n", rank);
        return 1;
    }

    float *ar_host = malloc(AR_COUNT * sizeof(float));
    void *ar_buf = NULL;

    if (use_nccl) {
        void *nccl = load_lib("libnccl.so.2");
        ncclGetUniqueId_t ncclGetId = dlsym(nccl, "ncclGetUniqueId");
        ncclCommInitRank_t ncclInit = dlsym(nccl, "ncclCommInitRank");
        ncclAR = dlsym(nccl, "ncclAllReduce");

        if (rank == 0) {
            ncclGetId((ncclUniqueId *)&sh->nccl_id);
            __sync_synchronize();
            sh->id_ready = 1;
        }
        while (!sh->id_ready) usleep(1000);

        if (cudaMalloc(&ar_buf, AR_COUNT * sizeof(float)) != 0 ||
            ncclInit(&comm, nranks, sh->nccl_id, rank) != ncclSuccess) {
            fprintf(stderr, "[rank %d] NCCL init failed\n", rank);
            return 1;
        }
    }

    printf("[rank %d pid=%d] device=%d persist=%zuMB nccl=%s\n",
           rank, getpid(), device, persist_bytes >> 20, use_nccl ? "live" : "off");
    fflush(stdout);
    sh->ready[rank] = 1;

    const float expect = (float)(nranks * (nranks + 1) / 2);

    while (!sh->stop) {
        if (use_nccl) {
            for (int i = 0; i < AR_COUNT; i++) ar_host[i] = (float)(rank + 1);
            cudaMemcpy(ar_buf, ar_host, AR_COUNT * sizeof(float), cudaMemcpyHostToDevice);
            if (ncclAR(ar_buf, ar_buf, AR_COUNT, ncclFloat, ncclSum, comm, NULL) != ncclSuccess) {
                fprintf(stderr, "[rank %d] allreduce failed\n", rank);
                sh->errors[rank]++;
                break;
            }
            cudaSync();
            cudaMemcpy(ar_host, ar_buf, AR_COUNT * sizeof(float), cudaMemcpyDeviceToHost);
            for (int i = 0; i < AR_COUNT; i += 4099) {
                if (ar_host[i] != expect) {
                    fprintf(stderr, "[rank %d] allreduce[%d]=%f expected %f\n",
                            rank, i, ar_host[i], expect);
                    sh->errors[rank]++;
                    break;
                }
            }
        }

        if (cudaMemcpy(check, persist, persist_bytes, cudaMemcpyDeviceToHost) != 0 ||
            memcmp(check, host, persist_bytes) != 0) {
            fprintf(stderr, "[rank %d] persistent buffer CORRUPTED at iter %ld\n",
                    rank, sh->iters[rank]);
            sh->errors[rank]++;
        }
        sh->iters[rank]++;
    }

    /* No ncclCommDestroy: in the NCCL case a rank that exits early would
     * leave its peers blocked in it. Process exit cleans up. */
    return sh->errors[rank] ? 1 : 0;
}

/* ── Parent ───────────────────────────────────────────────────────────── */

static int run_tool(const char *tool, const char *action, const pid_t *pids)
{
    char cmd[4096];
    int off = snprintf(cmd, sizeof(cmd), "%s %s", tool, action);
    for (int r = 0; r < nranks; r++)
        off += snprintf(cmd + off, sizeof(cmd) - off, " %d", pids[r]);
    printf("$ %s\n", cmd);
    fflush(stdout);
    return system(cmd) == 0 ? 0 : -1;
}

/* Count of ranks reported in `state` by the tool. */
static int count_state(const char *tool, const pid_t *pids, const char *state)
{
    char cmd[4096], line[256], want[64];
    int off = snprintf(cmd, sizeof(cmd), "%s state", tool);
    for (int r = 0; r < nranks; r++)
        off += snprintf(cmd + off, sizeof(cmd) - off, " %d", pids[r]);
    snprintf(want, sizeof(want), "state=%s\n", state);

    FILE *p = popen(cmd, "r");
    if (!p) return -1;
    int count = 0;
    while (fgets(line, sizeof(line), p)) {
        const char *s = strstr(line, "state=");
        if (s && strcmp(s, want) == 0) count++;
    }
    pclose(p);
    return count;
}

static double now_s(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec + ts.tv_nsec / 1e9;
}

int main(int argc, char **argv)
{
    if (argc < 2) {
        fprintf(stderr, "Usage: %s <nvsnap-gpu-suspend> [nranks=2] [persist_mb=256] [cycles=3] [suspend]\n", argv[0]);
        return 1;
    }
    const char *tool = argv[1];
    if (argc > 2) nranks = atoi(argv[2]);
    if (argc > 3) persist_bytes = (size_t)atoi(argv[3]) << 20;
    int cycles = argc > 4 ? atoi(argv[4]) : 3;
    int susp = argc > 5 && !strcmp(argv[5], "suspend");
    const char *save = susp ? "suspend" : "full-save", *restore = susp ? "resume" : "full-restore";
    if (nranks < 1 || nranks > MAX_RANKS) { fprintf(stderr, "nranks must be 1..%d\n", MAX_RANKS); return 1; }

    printf("=== nvsnap-gpu-suspend multi-rank test ===\n");

    /* Query the GPU count in a throwaway child: the parent must not
     * initialize CUDA before forking the ranks. */
    int ndev = 0;
    {
        int fds[2];
        if (pipe(fds) != 0) return 1;
        pid_t c = fork();
        if (c == 0) {
            void *cudart = load_lib("libcudart.so");
            cudaGetDeviceCount_t getCount = cudart ? dlsym(cudart, "cudaGetDeviceCount") : NULL;
            int n = 0;
            if (getCount) getCount(&n);
            if (write(fds[1], &n, sizeof(n)) != sizeof(n)) _exit(1);
            _exit(0);
        }
        if (read(fds[0], &ndev, sizeof(ndev)) != sizeof(ndev)) ndev = 0;
        waitpid(c, NULL, 0);
        close(fds[0]);
        close(fds[1]);
    }
    if (ndev < 1) { fprintf(stderr, "No CUDA devices\n"); return 1; }

    void *nccl = dlopen("libnccl.so.2", RTLD_LAZY);
    use_nccl = nccl && nranks > 1 && ndev >= nranks;
    if (nccl) dlclose(nccl);
    printf("ranks=%d gpus=%d nccl=%s persist=%zuMB cycles=%d\n\n", nranks, ndev,
           use_nccl ? "live" : (nccl ? "off (fewer GPUs than ranks)" : "off (libnccl.so.2 not found)"),
           persist_bytes >> 20, cycles);
    fflush(stdout); /* don't duplicate buffered output into forked ranks */

    struct shared *sh = mmap(NULL, sizeof(*sh), PROT_READ | PROT_WRITE,
                             MAP_SHARED | MAP_ANONYMOUS, -1, 0);
    memset(sh, 0, sizeof(*sh));

    pid_t pids[MAX_RANKS];
    for (int r = 0; r < nranks; r++) {
        pids[r] = fork();
        if (pids[r] == 0) _exit(run_rank(r, r % ndev, sh));
    }

    /* Wait for all ranks to be running. */
    double t0 = now_s();
    for (int r = 0; r < nranks; r++) {
        while (!sh->ready[r]) {
            if (now_s() - t0 > 120) { fprintf(stderr, "ranks did not start\n"); goto fail; }
            usleep(10000);
        }
    }
    sleep(1);

    for (int c = 1; c <= cycles; c++) {
        long before[MAX_RANKS];
        printf("\n--- cycle %d/%d ---\n", c, cycles);

        double ts = now_s();
        if (run_tool(tool, save, pids) < 0) { fprintf(stderr, "FAIL: %s\n", save); goto fail; }
        printf("%s: %.2fs\n", save, now_s() - ts);

        int n = count_state(tool, pids, "CHECKPOINTED");
        if (n != nranks) { fprintf(stderr, "FAIL: %d/%d ranks CHECKPOINTED\n", n, nranks); goto fail; }

        /* Ranks must be frozen while checkpointed. */
        for (int r = 0; r < nranks; r++) before[r] = sh->iters[r];
        sleep(1);
        for (int r = 0; r < nranks; r++) {
            if (sh->iters[r] != before[r]) {
                fprintf(stderr, "FAIL: rank %d progressed while checkpointed\n", r);
                goto fail;
            }
        }

        ts = now_s();
        if (run_tool(tool, restore, pids) < 0) { fprintf(stderr, "FAIL: %s\n", restore); goto fail; }
        printf("%s: %.2fs\n", restore, now_s() - ts);

        n = count_state(tool, pids, "RUNNING");
        if (n != nranks) { fprintf(stderr, "FAIL: %d/%d ranks RUNNING\n", n, nranks); goto fail; }

        /* Every rank must make progress (and verify its data) again. */
        for (int r = 0; r < nranks; r++) before[r] = sh->iters[r];
        t0 = now_s();
        for (int r = 0; r < nranks; r++) {
            while (sh->iters[r] < before[r] + 2) {
                if (now_s() - t0 > 30) { fprintf(stderr, "FAIL: rank %d stuck after restore\n", r); goto fail; }
                usleep(10000);
            }
        }
        for (int r = 0; r < nranks; r++) {
            if (sh->errors[r]) { fprintf(stderr, "FAIL: rank %d reported errors\n", r); goto fail; }
            printf("rank %d: %ld iterations, 0 errors\n", r, sh->iters[r]);
        }
    }

    sh->stop = 1;
    int failed = 0;
    for (int r = 0; r < nranks; r++) {
        int st = 0;
        waitpid(pids[r], &st, 0);
        if (!WIFEXITED(st) || WEXITSTATUS(st) != 0) failed++;
    }
    printf("\n=== %s ===\n", failed ? "FAIL" : "PASS");
    return failed ? 1 : 0;

fail:
    for (int r = 0; r < nranks; r++) kill(pids[r], SIGKILL);
    for (int r = 0; r < nranks; r++) waitpid(pids[r], NULL, 0);
    printf("\n=== FAIL ===\n");
    return 1;
}

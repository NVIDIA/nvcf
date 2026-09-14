/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Tests the ordering of `--action save` across several pids.
 *
 * The order is the whole hypothesis. Locking rank 0 while rank 1 still runs
 * leaves rank 1 free to post a collective that rank 0 will never service, and
 * rank 1's own lock then waits on an operation that cannot complete -- the
 * shape of the "hangs on 2nd rank (lock timeout)" failure recorded when
 * multi-GPU capture was last attempted. If save ever degrades to
 * lock+checkpoint per pid it reintroduces exactly that window, and it would do
 * so silently: every call still succeeds on a single-GPU workload.
 *
 * Stubs record the call sequence, so no GPU or driver is involved.
 *
 * Run: docker/agent/tests/run_save_order_test.sh
 */
#define main nvsnap_cuda_checkpoint_main
#include "../nvsnap-cuda-checkpoint.c"
#undef main

/* ---- recording stubs --------------------------------------------------- */
#define MAX_CALLS 64
static char g_calls[MAX_CALLS][32];
static int g_ncalls;

/* pid whose lock or checkpoint should fail, 0 for none */
static int g_fail_lock_pid;
static int g_fail_ckpt_pid;

static void record(const char *verb, int pid)
{
    if (g_ncalls < MAX_CALLS)
        snprintf(g_calls[g_ncalls++], 32, "%s(%d)", verb, pid);
}

CUresult cuGetErrorString(CUresult r, const char **s) { (void)r; *s = "stub"; return CUDA_SUCCESS; }
CUresult cuInit(unsigned int f) { (void)f; return CUDA_SUCCESS; }
CUresult cuDeviceGetCount(int *n) { *n = 0; return CUDA_SUCCESS; }
CUresult cuDeviceGet(CUdevice *d, int o) { (void)d; (void)o; return CUDA_ERROR_INVALID_DEVICE; }
CUresult cuDeviceGetUuid(CUuuid *u, CUdevice d) { (void)u; (void)d; return CUDA_ERROR_INVALID_DEVICE; }

CUresult cuCheckpointProcessLock(int pid, CUcheckpointLockArgs *a)
{
    (void)a;
    record("lock", pid);
    return pid == g_fail_lock_pid ? CUDA_ERROR_NOT_READY : CUDA_SUCCESS;
}

CUresult cuCheckpointProcessCheckpoint(int pid, CUcheckpointCheckpointArgs *a)
{
    (void)a;
    record("ckpt", pid);
    return pid == g_fail_ckpt_pid ? CUDA_ERROR_NOT_READY : CUDA_SUCCESS;
}

CUresult cuCheckpointProcessUnlock(int pid, CUcheckpointUnlockArgs *a)
{
    (void)a;
    record("unlock", pid);
    return CUDA_SUCCESS;
}

CUresult cuCheckpointProcessRestore(int pid, CUcheckpointRestoreArgs *a) { (void)a; record("restore", pid); return CUDA_SUCCESS; }
CUresult cuCheckpointProcessGetState(int p, CUprocessState *s) { (void)p; *s = CU_PROCESS_STATE_RUNNING; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessGetRestoreThreadId(int p, int *t) { (void)p; *t = 1; return CUDA_SUCCESS; }

/* ---- harness ---------------------------------------------------------- */
static int passed, failed;

static void ok(const char *name, int cond)
{
    if (cond) { passed++; printf("  ok   %s\n", name); }
    else      { failed++; printf("  FAIL %s\n", name); }
}

static void reset(int fail_lock, int fail_ckpt)
{
    g_ncalls = 0;
    g_pid_count = 0;
    g_fail_lock_pid = fail_lock;
    g_fail_ckpt_pid = fail_ckpt;
}

static const char *seq(void)
{
    static char buf[MAX_CALLS * 32];
    buf[0] = '\0';
    for (int i = 0; i < g_ncalls; i++) {
        if (i) strcat(buf, " ");
        strcat(buf, g_calls[i]);
    }
    return buf;
}

static void expect_seq(const char *name, const char *want)
{
    const char *got = seq();
    int cond = !strcmp(got, want);
    ok(name, cond);
    if (!cond)
        printf("       want: %s\n       got:  %s\n", want, got);
}

int main(void)
{
    printf("save ordering\n");

    /* The load-bearing assertion: every rank locked before any is
     * checkpointed, not lock+checkpoint per rank. */
    reset(0, 0);
    add_pids("101,102,103");
    ok("save succeeds with three ranks", do_save_all(0) == 0);
    expect_seq("locks all ranks before checkpointing any",
               "lock(101) lock(102) lock(103) ckpt(101) ckpt(102) ckpt(103)");

    /* A failed attempt must leave the job running, not half-locked: a rank
     * left LOCKED is a hung workload, which is worse than a failed capture. */
    reset(102, 0);
    add_pids("101,102,103");
    ok("save fails when a lock fails", do_save_all(0) != 0);
    expect_seq("rolls back already-locked ranks and checkpoints nothing",
               "lock(101) lock(102) unlock(101)");

    /* Failure on the first rank: nothing was locked, so nothing to undo. */
    reset(101, 0);
    add_pids("101,102");
    ok("save fails when the first lock fails", do_save_all(0) != 0);
    expect_seq("no rollback needed when the first lock fails", "lock(101)");

    /* Checkpoint failure stops immediately rather than continuing through the
     * remaining ranks, whose images would belong to a capture that cannot
     * complete anyway. Locks are deliberately left in place: the ranks already
     * checkpointed are recoverable only by restoring them, which the caller
     * drives. */
    reset(0, 102);
    add_pids("101,102,103");
    ok("save fails when a checkpoint fails", do_save_all(0) != 0);
    expect_seq("stops at the failing checkpoint",
               "lock(101) lock(102) lock(103) ckpt(101) ckpt(102)");

    /* Single pid must behave exactly as the plugin's existing calls expect. */
    reset(0, 0);
    add_pids("101");
    ok("single pid still succeeds", do_save_all(0) == 0);
    expect_seq("single pid is lock then checkpoint", "lock(101) ckpt(101)");

    /* pid list parsing: repeated flags and comma forms must be equivalent. */
    reset(0, 0);
    add_pids("7");
    add_pids("8,9");
    ok("accumulates repeated and comma-separated pids",
       g_pid_count == 3 && g_pids[0] == 7 && g_pids[1] == 8 && g_pids[2] == 9);

    reset(0, 0);
    ok("rejects a non-numeric pid", add_pids("abc") != 0);

    reset(0, 0);
    ok("rejects a negative pid", add_pids("-5") != 0);

    printf("\n%d passed, %d failed\n", passed, failed);
    return failed ? 1 : 0;
}

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * nvsnap-gpu-suspend — GPU checkpoint/restore via CUDA Checkpoint API.
 *
 * Uses the official cuCheckpointProcess* functions from the CUDA Driver API.
 * No ioctl replay, no cuda-checkpoint CLI.
 *
 * Usage:
 *   nvsnap-gpu-suspend [--timeout-ms N] <action> <pid> [pid ...]
 *
 *   state         Query state of each pid
 *   lock          Lock (pause CUDA) each pid
 *   checkpoint    Save GPU→host for each pid (after lock)
 *   restore       Reload host→GPU for each pid (after CRIU)
 *   unlock        Resume CUDA in each pid
 *   full-save     Lock ALL pids, then checkpoint ALL pids.
 *                 On any failure, every pid is rolled back to RUNNING.
 *   full-restore  Restore ALL pids, then unlock ALL pids.
 *                 Pids are only unlocked once every pid is restored, so
 *                 NCCL peers never resume without each other.
 *   suspend       full-save, but also freeze every thread of every pid
 *                 (ptrace) except the driver's restore thread, and keep
 *                 them frozen until `resume`. Returns once all pids are
 *                 CHECKPOINTED; a background holder keeps the freeze.
 *   stop          After suspend: hand the pids over to CRIU. Every thread
 *                 is left SIGSTOPped (CRIU dumps and restores them stopped)
 *                 and the holder exits.
 *   resume        Restore + unlock all pids, then let them run. With a
 *                 holder (suspend) it asks the holder; otherwise (after
 *                 stop + CRIU restore, or full-save) it freezes every thread
 *                 except the restore thread itself, SIGCONTs, restores.
 *   gpus [FILE]   Print (or write to FILE) the UUIDs of the GPUs visible here.
 *
 * --cache DIR (suspend, resume): node-local cache of the chunk store (e.g. on
 * NVMe; same layout): saved chunks are copied into it, and loads read it
 * first. A load does not fill it (that would slow the restore down):
 * cache-prefetch does, e.g. in the background once resumed. The cache is
 * never the only copy.
 *   cache-prefetch S C DIR   copy checkpoint C's chunks from store S into DIR
 *   cache-gc DIR MAX-GiB     LRU-evict DIR's chunks to 80% of MAX-GiB
 *
 * --store S --ckpt-dir C (suspend): processes using libnvsnap_gpushare save the
 * GPU memory it saves at "release" to the chunk store S (chunks shared by
 * every checkpoint in S, e.g. a model's weights, are stored once; C lists
 * this checkpoint's), all at once, instead of keeping it in host memory: a
 * CRIU image of them then needs S at restore.
 *
 * --gpu-map FILE (resume/restore/full-restore): FILE holds the `gpus` output
 * from where the checkpoint was taken; GPU i in FILE is restored onto the
 * i-th GPU visible here (e.g. a new pod got different physical GPUs).
 *
 * Why suspend: a CHECKPOINTED process has its pinned host buffers unmapped
 * too, but its CPU threads still run. Any CPU access to those buffers
 * (e.g. vLLM staging the next request) segfaults the process. Only CUDA
 * calls block. suspend keeps the CPU side frozen while checkpointed.
 * Needs CAP_SYS_PTRACE (or ptrace_scope=0) to attach to the pids.
 *
 * GPU memory shared between processes with cuMem (NCCL P2P) cannot be
 * restored by the driver. Run the workload with LD_PRELOAD=
 * libnvsnap_gpushare.so: suspend/resume then ask each process to release its
 * shared mappings (and multicast objects, page-locked host memory) before
 * lock (launches held back) and to re-create them at the same VA after
 * restore (see gpushare.h). Legacy CUDA IPC of memory not
 * allocated under libnvsnap_gpushare is not supported.
 *
 * Multi-GPU (TP/PP workers): pass every rank's pid in ONE invocation.
 * Locks are taken concurrently with a timeout: a rank blocked in a
 * collective whose peer is already locked cannot drain its work, so a
 * sequential lock-then-checkpoint loop can hang forever. With a timeout
 * the lock fails, everything is rolled back, and the caller can retry.
 */
#define _GNU_SOURCE  /* O_DIRECT */
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <stddef.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ptrace.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#include <cuda.h>

#include "gpushare.h"

static __typeof__(cuInit) *fn_init;
static __typeof__(cuGetErrorName) *fn_errname;
static __typeof__(cuCheckpointProcessLock) *fn_lock;
static __typeof__(cuCheckpointProcessCheckpoint) *fn_ckpt;
static __typeof__(cuCheckpointProcessRestore) *fn_restore;
static __typeof__(cuCheckpointProcessUnlock) *fn_unlock;
static __typeof__(cuCheckpointProcessGetState) *fn_state;
static __typeof__(cuCheckpointProcessGetRestoreThreadId) *fn_restore_tid;

static int load_api(void)
{
    void *h = dlopen("libcuda.so.1", RTLD_LAZY);
    if (!h) { fprintf(stderr, "Cannot load libcuda.so.1\n"); return -1; }

    fn_init = dlsym(h, "cuInit");
    fn_errname = dlsym(h, "cuGetErrorName");
    fn_lock = dlsym(h, "cuCheckpointProcessLock");
    fn_ckpt = dlsym(h, "cuCheckpointProcessCheckpoint");
    fn_restore = dlsym(h, "cuCheckpointProcessRestore");
    fn_unlock = dlsym(h, "cuCheckpointProcessUnlock");
    fn_state = dlsym(h, "cuCheckpointProcessGetState");
    fn_restore_tid = dlsym(h, "cuCheckpointProcessGetRestoreThreadId");

    if (!fn_init || !fn_lock || !fn_ckpt || !fn_restore || !fn_unlock || !fn_state) {
        fprintf(stderr, "CUDA Checkpoint API not available (need driver 555+)\n");
        return -1;
    }

    /* Restore requires cuInit (or persistence mode) in the caller. */
    CUresult r = fn_init(0);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "cuInit failed: %d\n", r);
        return -1;
    }
    return 0;
}

static const char *err_name(CUresult r)
{
    const char *s = NULL;
    if (fn_errname && fn_errname(r, &s) == CUDA_SUCCESS && s)
        return s;
    return "UNKNOWN_ERROR";
}

static const char *state_name(CUprocessState s)
{
    switch (s) {
    case CU_PROCESS_STATE_RUNNING: return "RUNNING";
    case CU_PROCESS_STATE_LOCKED: return "LOCKED";
    case CU_PROCESS_STATE_CHECKPOINTED: return "CHECKPOINTED";
    case CU_PROCESS_STATE_FAILED: return "FAILED";
    default: return "UNKNOWN";
    }
}

static int thread_stopped(int pid, int tid)
{
    char path[96], buf[512];
    snprintf(path, sizeof(path), "/proc/%d/task/%d/stat", pid, tid);
    FILE *fp = fopen(path, "r");
    if (!fp) return 0;
    char *p = fgets(buf, sizeof(buf), fp) ? strrchr(buf, ')') : NULL;
    fclose(fp);
    return p && p[1] == ' ' && (p[2] == 'T' || p[2] == 't');
}

/* 1 if pid's CUDA restore thread is stopped. CUDA state queries are served
 * by that thread and block forever while it is stopped (SIGSTOP / CRIU). */
static int is_stopped(int pid)
{
    int tid = -1;
    if (!fn_restore_tid || fn_restore_tid(pid, &tid) != CUDA_SUCCESS || tid <= 0)
        return 0;
    return thread_stopped(pid, tid);
}

static int get_state(int pid, CUprocessState *s)
{
    if (is_stopped(pid)) {
        fprintf(stderr, "pid=%d is SIGSTOPped (after stop / CRIU restore); "
                        "CUDA state unavailable until resume\n", pid);
        return -1;
    }
    CUresult r = fn_state(pid, s);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "pid=%d get_state failed: %s\n", pid, err_name(r));
        return -1;
    }
    return 0;
}

static int do_lock(int pid, unsigned timeout_ms)
{
    CUcheckpointLockArgs args;
    memset(&args, 0, sizeof(args));
    args.timeoutMs = timeout_ms;
    CUresult r = fn_lock(pid, &args);
    printf("pid=%d lock=%s\n", pid, err_name(r));
    return r == CUDA_SUCCESS ? 0 : -1;
}

static int do_checkpoint(int pid)
{
    CUresult r = fn_ckpt(pid, NULL);
    printf("pid=%d checkpoint=%s\n", pid, err_name(r));
    return r == CUDA_SUCCESS ? 0 : -1;
}

static __typeof__(cuDeviceGetCount) *fn_dev_count;
static __typeof__(cuDeviceGet) *fn_dev_get;
static CUresult (*fn_dev_uuid)(CUuuid *, CUdevice);

static void uuid_str(const CUuuid *u, char out[41])
{
    const unsigned char *b = (const unsigned char *)u->bytes;
    snprintf(out, 41, "GPU-%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-"
             "%02x%02x%02x%02x%02x%02x", b[0], b[1], b[2], b[3], b[4], b[5],
             b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15]);
}

#if CUDA_VERSION >= 13000
static int parse_uuid(const char *str, CUuuid *u)
{
    unsigned char *b = (unsigned char *)u->bytes;
    int i = 0;
    if (strncmp(str, "GPU-", 4) == 0) str += 4;
    for (; *str && i < 16; str++) {
        if (*str == '-') continue;
        unsigned v;
        if (sscanf(str, "%2x", &v) != 1) return -1;
        b[i++] = (unsigned char)v;
        str++;
    }
    return i == 16 ? 0 : -1;
}
#endif

static int visible_gpus(CUuuid *out, int max)
{
    void *h = dlopen("libcuda.so.1", RTLD_LAZY);
    fn_dev_count = dlsym(h, "cuDeviceGetCount");
    fn_dev_get = dlsym(h, "cuDeviceGet");
    fn_dev_uuid = dlsym(h, "cuDeviceGetUuid_v2");
    if (!fn_dev_uuid) fn_dev_uuid = dlsym(h, "cuDeviceGetUuid");
    int n = 0;
    if (!fn_dev_count || !fn_dev_get || !fn_dev_uuid || fn_dev_count(&n) != CUDA_SUCCESS)
        return -1;
    for (int i = 0; i < n && i < max; i++) {
        CUdevice d;
        if (fn_dev_get(&d, i) != CUDA_SUCCESS || fn_dev_uuid(&out[i], d) != CUDA_SUCCESS)
            return -1;
    }
    return n < max ? n : max;
}

#define MAX_GPUS 64
static const char *gpu_map_file;
static const char *store_dir, *ckpt_dir, *cache_dir, *fabric_dir;

static int do_restore(int pid)
{
    void *args = NULL;
#if CUDA_VERSION >= 13000
    CUcheckpointGpuPair pairs[MAX_GPUS];
    CUcheckpointRestoreArgs rargs;
    memset(&rargs, 0, sizeof(rargs));
    if (gpu_map_file) {
        CUuuid now[MAX_GPUS];
        int n_now = visible_gpus(now, MAX_GPUS), n = 0;
        char line[128];
        FILE *fp = fopen(gpu_map_file, "r");
        if (!fp || n_now < 0) {
            fprintf(stderr, "cannot read %s or query GPUs\n", gpu_map_file);
            if (fp) fclose(fp);
            return -1;
        }
        while (n < MAX_GPUS && fgets(line, sizeof(line), fp)) {
            if (parse_uuid(line, &pairs[n].oldUuid) < 0) continue;
            if (n >= n_now) { fprintf(stderr, "%s lists more GPUs than visible\n", gpu_map_file); fclose(fp); return -1; }
            pairs[n].newUuid = now[n];
            char a[41], b[41];
            uuid_str(&pairs[n].oldUuid, a);
            uuid_str(&pairs[n].newUuid, b);
            printf("pid=%d gpu map %s -> %s\n", pid, a, b);
            n++;
        }
        fclose(fp);
        rargs.gpuPairs = pairs;
        rargs.gpuPairsCount = n;
        args = &rargs;
    }
#else
    if (gpu_map_file) {
        fprintf(stderr, "--gpu-map needs CUDA 13 headers (CUcheckpointGpuPair)\n");
        return -1;
    }
#endif
    CUresult r = fn_restore(pid, args);
    printf("pid=%d restore=%s\n", pid, err_name(r));
    return r == CUDA_SUCCESS ? 0 : -1;
}

static int do_unlock(int pid)
{
    CUresult r = fn_unlock(pid, NULL);
    printf("pid=%d unlock=%s\n", pid, err_name(r));
    return r == CUDA_SUCCESS ? 0 : -1;
}

/* ── Concurrent lock ──────────────────────────────────────────────────── */

struct lock_job {
    pthread_t thread;
    int started;  /* thread created: join it, then read ret */
    int pid;
    unsigned timeout_ms;
    int ret;
};

static void *lock_thread(void *arg)
{
    struct lock_job *j = arg;
    j->ret = do_lock(j->pid, j->timeout_ms);
    return NULL;
}

/* Lock every RUNNING pid concurrently; already-LOCKED pids are skipped. */
static int lock_all(const int *pids, int n, unsigned timeout_ms)
{
    struct lock_job *jobs = calloc(n, sizeof(*jobs));
    int failures = 0;

    for (int i = 0; i < n; i++) {
        CUprocessState s;
        jobs[i].pid = pids[i];
        jobs[i].timeout_ms = timeout_ms;
        if (get_state(pids[i], &s) < 0) { failures++; continue; }
        if (s == CU_PROCESS_STATE_LOCKED) continue;
        if (s != CU_PROCESS_STATE_RUNNING) {
            fprintf(stderr, "pid=%d cannot lock from state %s\n",
                    pids[i], state_name(s));
            failures++;
            continue;
        }
        if (pthread_create(&jobs[i].thread, NULL, lock_thread, &jobs[i]) != 0) {
            fprintf(stderr, "pid=%d pthread_create failed\n", pids[i]);
            failures++;
            continue;
        }
        jobs[i].started = 1;
    }
    for (int i = 0; i < n; i++) {
        if (!jobs[i].started) continue;
        pthread_join(jobs[i].thread, NULL);  /* ret is the thread's only after this */
        if (jobs[i].ret != 0) failures++;
    }

    free(jobs);
    return failures ? -1 : 0;
}

/* Bring every pid back to RUNNING after a failed full-save. */
/* Returns the number of pids left not RUNNING. */
static int rollback(const int *pids, int n)
{
    int bad = 0;
    fprintf(stderr, "rolling back %d pid(s) to RUNNING\n", n);
    for (int i = 0; i < n; i++) {
        CUprocessState s;
        if (get_state(pids[i], &s) < 0) { bad++; continue; }
        if (s == CU_PROCESS_STATE_CHECKPOINTED) {
            if (do_restore(pids[i]) < 0) { bad++; continue; }
            s = CU_PROCESS_STATE_LOCKED;
        }
        if (s == CU_PROCESS_STATE_LOCKED && do_unlock(pids[i]) < 0) bad++;
    }
    return bad;
}

/* Run fn on every pid at once (the driver checkpoints and restores each
 * process independently); returns the number of failures. */
struct pid_job { int pid; int (*fn)(int); int r; };

static void *pid_thread(void *arg)
{
    struct pid_job *j = arg;
    j->r = j->fn(j->pid);
    return NULL;
}

static int each_par(const int *pids, int n, int (*fn)(int))
{
    struct pid_job *j = calloc(n, sizeof(*j));
    pthread_t *t = calloc(n, sizeof(*t));
    int failures = 0;
    for (int i = 0; i < n; i++) {
        j[i] = (struct pid_job){ pids[i], fn, -1 };
        if (pthread_create(&t[i], NULL, pid_thread, &j[i]) != 0) pid_thread(&j[i]), t[i] = 0;
    }
    for (int i = 0; i < n; i++) {
        if (t[i]) pthread_join(t[i], NULL);
        failures += j[i].r < 0;
    }
    free(j);
    free(t);
    return failures;
}

static int full_save(const int *pids, int n, unsigned timeout_ms)
{
    if (lock_all(pids, n, timeout_ms) < 0) {
        rollback(pids, n);
        return -1;
    }
    for (int i = 0; i < n; i++) {
        if (do_checkpoint(pids[i]) < 0) {
            rollback(pids, n);
            return -1;
        }
    }
    return 0;
}

static int full_restore(const int *pids, int n)
{
    /* Restore every CHECKPOINTED pid; LOCKED pids were already restored
     * and RUNNING ones also unlocked (e.g. by a previous run that failed
     * later, in "remap"), so re-running is safe and lets a resume retry. */
    int failures = 0, *todo = calloc(n, sizeof(int)), nt = 0;
    for (int i = 0; i < n; i++) {
        CUprocessState s;
        if (get_state(pids[i], &s) < 0) { failures++; continue; }
        if (s == CU_PROCESS_STATE_LOCKED || s == CU_PROCESS_STATE_RUNNING) continue;
        if (s != CU_PROCESS_STATE_CHECKPOINTED) {
            fprintf(stderr, "pid=%d cannot restore from state %s\n",
                    pids[i], state_name(s));
            failures++;
            continue;
        }
        todo[nt++] = pids[i];
    }
    failures += each_par(todo, nt, do_restore);
    free(todo);
    if (failures) {
        fprintf(stderr, "%d pid(s) not restored; restored pids stay LOCKED "
                        "(re-run full-restore to retry)\n", failures);
        return -1;
    }

    for (int i = 0; i < n; i++) {
        CUprocessState s;
        if (get_state(pids[i], &s) < 0) failures++;
        else if (s == CU_PROCESS_STATE_LOCKED && do_unlock(pids[i]) < 0) failures++;
    }
    return failures ? -1 : 0;
}

/* ── Thread freeze (ptrace) ───────────────────────────────────────────── */

struct frozen {
    int *tids;
    int *sigs;  /* signal caught while stopping, re-injected on thaw */
    int n, cap;
};

static int frozen_has(const struct frozen *f, int tid)
{
    for (int i = 0; i < f->n; i++)
        if (f->tids[i] == tid) return 1;
    return 0;
}

/* 1 if tid is libnvsnap_gpushare's control thread. */
static int is_ctl_thread(int pid, int tid)
{
    char path[64], comm[32] = "";
    snprintf(path, sizeof(path), "/proc/%d/task/%d/comm", pid, tid);
    FILE *fp = fopen(path, "r");
    if (!fp) return 0;
    if (!fgets(comm, sizeof(comm), fp)) comm[0] = 0;
    fclose(fp);
    comm[strcspn(comm, "\n")] = 0;
    return !strcmp(comm, GPUSHARE_THREAD);
}

/* Stop every thread of pid except the CUDA restore thread, which the
 * driver needs to run checkpoint/restore inside the process, and the
 * libnvsnap_gpushare control thread, which releases/remaps shared memory. */
static int freeze_pid(int pid, struct frozen *f)
{
    int keep = -1;
    CUresult r = fn_restore_tid ? fn_restore_tid(pid, &keep) : CUDA_ERROR_NOT_SUPPORTED;
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "pid=%d get restore thread failed: %s\n", pid, err_name(r));
        return -1;
    }

    char path[64];
    snprintf(path, sizeof(path), "/proc/%d/task", pid);

    /* Re-scan until stable: a thread may spawn another before it is stopped. */
    for (int added = 1; added; ) {
        added = 0;
        DIR *d = opendir(path);
        if (!d) { fprintf(stderr, "pid=%d opendir %s: %s\n", pid, path, strerror(errno)); return -1; }
        struct dirent *e;
        while ((e = readdir(d)) != NULL) {
            int tid = atoi(e->d_name);
            if (tid <= 0 || tid == keep || frozen_has(f, tid) || is_ctl_thread(pid, tid)) continue;
            if (ptrace(PTRACE_SEIZE, tid, 0, 0) != 0) {
                if (errno == ESRCH) continue; /* thread exited */
                fprintf(stderr, "pid=%d tid=%d ptrace seize: %s "
                                "(need CAP_SYS_PTRACE)\n", pid, tid, strerror(errno));
                closedir(d);
                return -1;
            }
            if (f->n == f->cap) {
                f->cap = f->cap ? 2 * f->cap : 64;
                f->tids = realloc(f->tids, f->cap * sizeof(int));
                f->sigs = realloc(f->sigs, f->cap * sizeof(int));
            }
            f->tids[f->n] = tid;
            f->sigs[f->n] = 0;
            f->n++;
            added = 1;

            int st;
            ptrace(PTRACE_INTERRUPT, tid, 0, 0);
            if (waitpid(tid, &st, __WALL) == tid && WIFSTOPPED(st) &&
                (st >> 16) != PTRACE_EVENT_STOP)
                f->sigs[f->n - 1] = WSTOPSIG(st); /* signal-delivery-stop */
        }
        closedir(d);
    }
    printf("pid=%d froze %d thread(s), restore thread %d left running\n",
           pid, f->n, keep);
    return 0;
}

static void thaw(struct frozen *f)
{
    for (int i = 0; i < f->n; i++)
        ptrace(PTRACE_DETACH, f->tids[i], 0, (void *)(long)f->sigs[i]);
    free(f->tids);
    free(f->sigs);
    memset(f, 0, sizeof(*f));
}

static void thaw_all(struct frozen *fz, int n)
{
    for (int i = 0; i < n; i++) thaw(&fz[i]);
}

/* ── Shared GPU memory (libnvsnap_gpushare control socket) ─────────────── */

/* Send cmd to pid's control thread. Returns 1 if pid has none (it shares
 * no GPU memory), 0 on "ok", -2 on "busy" (retry), -1 on error. */
static int ctl(int pid, const char *cmd)
{
    struct sockaddr_un a = { .sun_family = AF_UNIX };
    int n = snprintf(a.sun_path + 1, sizeof(a.sun_path) - 1, GPUSHARE_SOCK_FMT, pid);
    socklen_t len = offsetof(struct sockaddr_un, sun_path) + 1 + n;
    int s = socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
    if (s < 0) return -1;
    if (connect(s, (struct sockaddr *)&a, len) != 0) {
        close(s);
        return 1;
    }
    /* An abstract name can be bound by anyone in the network namespace:
     * talk only to pid itself (so this tool must run in its pid namespace). */
    struct ucred cr;
    socklen_t cl = sizeof(cr);
    if (getsockopt(s, SOL_SOCKET, SO_PEERCRED, &cr, &cl) != 0 || cr.pid != pid) {
        close(s);
        printf("pid=%d %s: err control socket @" GPUSHARE_SOCK_FMT " is not pid %d's (peer pid %d)\n",
               pid, cmd, pid, pid, cl == sizeof(cr) ? cr.pid : -1);
        return -1;
    }
    /* "release"/"load" move the saved GPU memory: allow for slow disks. */
    int slow = !strncmp(cmd, "release", 7) || !strncmp(cmd, "load", 4);
    struct timeval tv = { slow ? 3600 : 120, 0 };
    setsockopt(s, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    char rep[256] = "err no reply (timeout)";
    if (send(s, cmd, strlen(cmd) + 1, MSG_NOSIGNAL) < 0) snprintf(rep, sizeof(rep), "err send: %s", strerror(errno));
    else {
        ssize_t r = recv(s, rep, sizeof(rep) - 1, 0);
        if (r > 0) rep[r] = 0;
        else snprintf(rep, sizeof(rep), "err no reply (%s)", r < 0 ? strerror(errno) : "closed");
    }
    close(s);
    printf("pid=%d %s: %s\n", pid, cmd, rep);
    if (!strncmp(rep, "err busy", 8)) return -2;
    return strncmp(rep, "ok", 2) ? -1 : 0;
}

/* Run cmd on every pid that shares GPU memory. Returns the first error
 * (see ctl), otherwise the number of pids that had a control socket. */
static int ctl_all(const int *pids, int n, const char *cmd)
{
    int count = 0;
    for (int i = 0; i < n; i++) {
        int r = ctl(pids[i], cmd);
        if (r < 0) return r;
        count += r == 0;
    }
    return count;
}

struct ctl_job { int pid; const char *cmd; int r; };

static void *ctl_thread(void *arg)
{
    struct ctl_job *j = arg;
    j->r = ctl(j->pid, j->cmd);
    return NULL;
}

/* ctl_all on every pid at once: for commands that do not talk to peers
 * ("release", "load"), so each process saves/loads its GPU memory in
 * parallel. */
static int ctl_all_par(const int *pids, int n, const char *cmd)
{
    struct ctl_job *j = calloc(n, sizeof(*j));
    pthread_t *t = calloc(n, sizeof(*t));
    int count = 0, err = 0;
    for (int i = 0; i < n; i++) {
        j[i] = (struct ctl_job){ pids[i], cmd, -1 };
        if (pthread_create(&t[i], NULL, ctl_thread, &j[i]) != 0) ctl_thread(&j[i]), t[i] = 0;
    }
    for (int i = 0; i < n; i++) {
        if (t[i]) pthread_join(t[i], NULL);
        if (j[i].r < 0 && !err) err = j[i].r;
        count += j[i].r == 0;
    }
    free(j);
    free(t);
    return err ? err : count;
}

/* Re-create shared mappings in three passes: every process re-creates the
 * GPU memory it saved ("load", all at once), then re-imports (and rejoins
 * its multicast groups) before any binds multicast memory, which blocks
 * until all devices have rejoined. "resume" reopens the launch gate. */
static int fabric_exchange(const int *pids, int n, char *remap, size_t len);

static int remap_all(const int *pids, int n)
{
    char load[512] = "load", remap[600] = "remap";
    if (cache_dir) snprintf(load, sizeof(load), "load %s", cache_dir);
    if (fabric_dir) {  /* a previous cycle's lists */
        char p[512];
        snprintf(p, sizeof(p), "%s/in", fabric_dir);
        unlink(p);
        snprintf(p, sizeof(p), "%s/out", fabric_dir);
        unlink(p);
    }
    int r = ctl_all_par(pids, n, load);
    if (r >= 0 && fabric_dir) r = fabric_exchange(pids, n, remap, sizeof(remap));
    if (r >= 0) r = ctl_all(pids, n, remap);
    return r < 0 ? r : ctl_all(pids, n, "resume");
}

/* Multi-node NVLink: memory shared across nodes is imported by fabric
 * handle, and fabric handles change across a restore. After "load", every
 * process has exported its shared memory again: collect old -> new into
 * DIR/out, then wait for DIR/in, the lists of all nodes merged by whoever
 * drives the nodes, and pass it to "remap". */
static int fabric_exchange(const int *pids, int n, char *remap, size_t len)
{
    char in[512], out[512], tmp[512], part[600], cmd[700], line[300];
    snprintf(in, sizeof(in), "%s/in", fabric_dir);
    snprintf(out, sizeof(out), "%s/out", fabric_dir);
    snprintf(tmp, sizeof(tmp), "%s/out.tmp", fabric_dir);
    FILE *o = fopen(tmp, "we");
    if (!o) { fprintf(stderr, "%s: %s\n", tmp, strerror(errno)); return -1; }
    int nh = 0;
    for (int i = 0; i < n; i++) {
        snprintf(part, sizeof(part), "%s/out.%d", fabric_dir, pids[i]);
        snprintf(cmd, sizeof(cmd), "fabmap %s", part);
        if (ctl(pids[i], cmd) < 0) { fclose(o); return -1; }
        FILE *f = fopen(part, "re");
        while (f && fgets(line, sizeof(line), f)) { fputs(line, o); nh++; }
        if (f) fclose(f);
        unlink(part);
    }
    if (fclose(o) != 0 || rename(tmp, out) != 0) { fprintf(stderr, "%s: %s\n", out, strerror(errno)); return -1; }
    printf("fabric map: %d handle(s) in %s; waiting for %s\n", nh, out, in);
    for (int t = 0; access(in, R_OK) != 0; t++) {
        if (t >= 6000) { fprintf(stderr, "no %s after 600 s\n", in); return -1; }
        usleep(100000);
    }
    snprintf(remap, len, "remap %s", in);
    return 0;
}

/* ── suspend / resume ─────────────────────────────────────────────────── */

#define STATE_DIR "/tmp/nvsnap-gpu-suspend"

/* The state directory has a predictable path in /tmp, where another user
 * could create it first and plant symlinks for this tool (often root) to
 * follow. Use it only if it is a directory owned by us that no one else can
 * write to, and open files in it relative to it, never through a symlink. */
static int state_dir(void)
{
    static int fd = -1;
    struct stat st;
    if (fd >= 0) return fd;
    if (mkdir(STATE_DIR, 0700) != 0 && errno != EEXIST) {
        fprintf(stderr, STATE_DIR ": %s\n", strerror(errno));
        return -1;
    }
    int d = open(STATE_DIR, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    if (d < 0 || fstat(d, &st) != 0 || st.st_uid != geteuid() || (st.st_mode & 077)) {
        fprintf(stderr, STATE_DIR ": not a directory of uid %d with mode 0700 (%s); refusing to use it\n",
                (int)geteuid(), d < 0 ? strerror(errno) : "wrong owner or mode");
        if (d >= 0) close(d);
        return -1;
    }
    return fd = d;
}

/* Name of pid's state file in the state directory. */
static void state_path(char *buf, size_t len, int pid, const char *ext)
{
    snprintf(buf, len, "%d.%s", pid, ext);
}

static int state_open(const char *name, int flags)
{
    int d = state_dir();
    return d < 0 ? -1 : openat(d, name, flags | O_NOFOLLOW | O_CLOEXEC, 0600);
}

static FILE *state_fopen(const char *name)
{
    int fd = state_open(name, O_RDONLY);
    FILE *fp = fd >= 0 ? fdopen(fd, "r") : NULL;
    if (fd >= 0 && !fp) close(fd);
    return fp;
}

static int state_exists(const char *name)
{
    int d = state_dir();
    return d >= 0 && faccessat(d, name, F_OK, AT_SYMLINK_NOFOLLOW) == 0;
}

static void state_unlink(const char *name)
{
    int d = state_dir();
    if (d >= 0) unlinkat(d, name, 0);
}

static void write_file(const char *name, const char *text)
{
    int fd = state_open(name, O_WRONLY | O_CREAT | O_TRUNC);
    if (fd < 0) return;
    ssize_t __attribute__((unused)) w = write(fd, text, strlen(text));
    close(fd);
}

/* Wait until every thread of pid is in stopped state ('T'). */
static int wait_all_stopped(int pid)
{
    char path[64], stat[512];
    snprintf(path, sizeof(path), "/proc/%d/task", pid);
    for (int tries = 0; tries < 100; tries++) {
        int running = 0;
        DIR *d = opendir(path);
        if (!d) return -1;
        struct dirent *e;
        while ((e = readdir(d)) != NULL) {
            if (e->d_name[0] == '.') continue;
            char sp[384];
            snprintf(sp, sizeof(sp), "%s/%s/stat", path, e->d_name);
            FILE *fp = fopen(sp, "r");
            if (!fp) continue;
            /* state is the field after "(comm)" */
            if (fgets(stat, sizeof(stat), fp)) {
                char *p = strrchr(stat, ')');
                if (p && p[1] == ' ' && p[2] != 'T' && p[2] != 't') running++;
            }
            fclose(fp);
        }
        closedir(d);
        if (!running) return 0;
        usleep(10000);
    }
    fprintf(stderr, "pid=%d: threads still running after SIGSTOP\n", pid);
    return -1;
}

/* Resume without a holder: pids are CHECKPOINTED and possibly SIGSTOPped
 * (after stop + CRIU restore). Freeze everything but the restore threads,
 * SIGCONT, restore + unlock, thaw. */
static int resume_unheld(const int *pids, int n)
{
    if (load_api() < 0) return -1;
    struct frozen *fz = calloc(n, sizeof(*fz));
    int rc = -1;
    for (int i = 0; i < n; i++)
        if (freeze_pid(pids[i], &fz[i]) < 0) goto out;
    for (int i = 0; i < n; i++) kill(pids[i], SIGCONT);
    for (int i = 0; i < n; i++)  /* wait for SIGCONT to reach the restore thread */
        for (int t = 0; t < 500 && is_stopped(pids[i]); t++) usleep(10000);
    if (full_restore(pids, n) < 0) goto out;
    if (remap_all(pids, n) < 0) {
        /* Restored and unlocked, but shared mappings are missing: running
         * would fault on them. Stay frozen; re-run resume. */
        for (int i = 0; i < n; i++) kill(pids[i], SIGSTOP);
        thaw_all(fz, n);
        fprintf(stderr, "remap failed; pids left SIGSTOPped\n");
        free(fz);
        return -1;
    }
    rc = 0;
out:
    if (rc == 0) {
        thaw_all(fz, n);
        printf("thawed all pids\n");
    } else {
        /* Do not let CHECKPOINTED threads run: leave them stopped. */
        for (int i = 0; i < n; i++) kill(pids[i], SIGSTOP);
        thaw_all(fz, n);
        fprintf(stderr, "resume failed; pids left SIGSTOPped (re-run resume)\n");
    }
    free(fz);
    return rc;
}

/* Holder: runs in the background, owns the ptrace freeze. */
/* A suspend failed: undo the driver lock or checkpoint (locked), then
 * what "release" dropped (shared), and say whether the workload runs. */
static void undo(const int *pids, int n, int locked, int shared)
{
    int bad = locked ? rollback(pids, n) : 0;
    if (shared && remap_all(pids, n) < 0) bad++;
    if (bad) fprintf(stderr, "suspend failed and so did the rollback: the workload may be stuck (errors above)\n");
    else fprintf(stderr, "suspend failed; rolled back, the workload runs as before\n");
}

/* Multi-node NVLink: a node that releases memory while another still runs
 * kernels that read it crashes those kernels, and a node's in-flight
 * collectives wait for the ranks other nodes hold. So each attempt to
 * quiesce is a vote: write DIR/q ("<attempt> ok" or "<attempt> busy") and
 * wait for DIR/d, written by whoever drives the nodes once every node has
 * voted: "<attempt> go" if all are quiesced, else "<attempt> retry".
 * Returns 1 for go, 0 for retry, -1 on timeout. */
static int node_vote(int attempt, int quiesced)
{
    char q[512], tmp[520], d[512], line[64];
    snprintf(q, sizeof(q), "%s/q", fabric_dir);
    snprintf(tmp, sizeof(tmp), "%s/q.tmp", fabric_dir);
    snprintf(d, sizeof(d), "%s/d", fabric_dir);
    FILE *fp = fopen(tmp, "we");
    if (!fp || fprintf(fp, "%d %s\n", attempt, quiesced ? "ok" : "busy") < 0 || fclose(fp) != 0 || rename(tmp, q) != 0) {
        fprintf(stderr, "%s: %s\n", q, strerror(errno));
        return -1;
    }
    for (int t = 0; t < 6000; t++) {
        int a = -1;
        char verdict[16] = "";
        if ((fp = fopen(d, "re"))) {
            if (fgets(line, sizeof(line), fp)) sscanf(line, "%d %15s", &a, verdict);
            fclose(fp);
        }
        if (a == attempt) return !strcmp(verdict, "go");
        usleep(100000);
    }
    fprintf(stderr, "no verdict in %s after 600 s\n", d);
    return -1;
}

static int holder(const int *pids, int n, unsigned timeout_ms, int report_fd)
{
    struct frozen *fz = calloc(n, sizeof(*fz));
    char path[256], buf[32];
    int ok = 0;

    if (load_api() < 0) goto out;

    /* Processes that share GPU memory drop their shared mappings first:
     * CUDA calls block once locked. "quiesce" holds the app's launches in
     * libnvsnap_gpushare's gate (until "resume") and checks every process can
     * release; only then does any process "release". Busy: GPU work waits on
     * work not launched yet (e.g. a peer's NCCL kernel) — reopen and retry. */
    int shared = 0;
    if (fabric_dir) {  /* a previous suspend's votes */
        snprintf(path, sizeof(path), "%s/q", fabric_dir);
        unlink(path);
        snprintf(path, sizeof(path), "%s/d", fabric_dir);
        unlink(path);
    }
    for (int tries = 0;; tries++) {
        int r = ctl_all(pids, n, "quiesce");
        if (r < 0) ctl_all(pids, n, "resume");
        if (r < 0 && r != -2) goto out;
        if (fabric_dir) {  /* every node must be quiesced before any releases */
            int v = node_vote(tries, r >= 0);
            if (v < 0) { if (r >= 0) ctl_all(pids, n, "resume"); goto out; }
            if (v == 1) { shared = r > 0; break; }
            if (r >= 0) ctl_all(pids, n, "resume");
        } else if (r >= 0) {
            shared = r > 0;
            break;
        }
        if (tries * 250 >= (int)timeout_ms) goto out;
        usleep(250000);
    }
    char release[600] = "release";
    if (store_dir) snprintf(release, sizeof(release), "release %s %s%s%s", store_dir, ckpt_dir,
                            cache_dir ? " " : "", cache_dir ? cache_dir : "");
    if (shared && ctl_all_par(pids, n, release) < 0) {
        undo(pids, n, 0, shared);
        goto out;
    }
    if (lock_all(pids, n, timeout_ms) < 0) {
        undo(pids, n, 1, shared);
        goto out;
    }
    for (int i = 0; i < n; i++) {
        if (freeze_pid(pids[i], &fz[i]) < 0) {
            thaw_all(fz, n);
            undo(pids, n, 1, shared);
            goto out;
        }
    }
    if (each_par(pids, n, do_checkpoint)) {
        undo(pids, n, 1, shared); /* restore runs on the (unfrozen) restore thread */
        thaw_all(fz, n);
        goto out;
    }
    ok = 1;

out:
    snprintf(buf, sizeof(buf), "%d\n", ok ? (int)getpid() : 0);
    for (int i = 0; ok && i < n; i++) {
        state_path(path, sizeof(path), pids[i], "holder");
        write_file(path, buf);
    }
    if (write(report_fd, ok ? "1" : "0", 1) != 1) { /* parent gone */ }
    close(report_fd);
    if (!ok) { free(fz); return 1; }

    /* Hold the freeze until resume (SIGUSR1/SIGTERM/SIGINT) or stop (SIGUSR2). */
    sigset_t set;
    sigemptyset(&set);
    sigaddset(&set, SIGUSR1);
    sigaddset(&set, SIGUSR2);
    sigaddset(&set, SIGTERM);
    sigaddset(&set, SIGINT);
    sigprocmask(SIG_BLOCK, &set, NULL);

    char result[256];
    state_path(result, sizeof(result), pids[0], "result");
    for (;;) {
        int sig;
        sigwait(&set, &sig);
        if (sig == SIGUSR2) {
            /* SIGSTOP first: the traced threads stop again as soon as they
             * are detached, and the untraced restore thread stops now. */
            for (int i = 0; i < n; i++) kill(pids[i], SIGSTOP);
            thaw_all(fz, n);
            int ok_stop = 1;
            for (int i = 0; i < n; i++) ok_stop &= wait_all_stopped(pids[i]) == 0;
            printf("stop: all pids SIGSTOPped, holder exiting (%s)\n", ok_stop ? "ok" : "FAILED");
            for (int i = 0; i < n; i++) {
                state_path(path, sizeof(path), pids[i], "holder");
                state_unlink(path);
            }
            write_file(result, ok_stop ? "ok\n" : "fail\n");
            free(fz);
            return ok_stop ? 0 : 1;
        }
        printf("resume requested (signal %d)\n", sig);
        if (full_restore(pids, n) == 0 && remap_all(pids, n) >= 0) break;
        /* Stay frozen: a thawed CHECKPOINTED process would crash. */
        write_file(result, "fail\n");
    }
    thaw_all(fz, n);
    printf("thawed all pids\n");
    for (int i = 0; i < n; i++) {
        state_path(path, sizeof(path), pids[i], "holder");
        state_unlink(path);
    }
    write_file(result, "ok\n");
    free(fz);
    return 0;
}

static void cat_file(const char *name, long from)
{
    char line[512];
    FILE *fp = state_fopen(name);
    if (!fp) return;
    fseek(fp, from, SEEK_SET);
    while (fgets(line, sizeof(line), fp)) fputs(line, stdout);
    fclose(fp);
}

static int suspend(const int *pids, int n, unsigned timeout_ms)
{
    char log[256], path[256];
    int fds[2];

    if (state_dir() < 0) return -1;
    state_path(path, sizeof(path), pids[0], "holder");
    if (state_exists(path)) {
        fprintf(stderr, "pid=%d already suspended (" STATE_DIR "/%s exists)\n", pids[0], path);
        return -1;
    }
    state_path(log, sizeof(log), pids[0], "log");
    if (pipe(fds) != 0) return -1;

    /* Fork before any CUDA call: CUDA is not usable across fork(). */
    pid_t child = fork();
    if (child < 0) return -1;
    if (child == 0) {
        close(fds[0]);
        setsid();
        int fd = state_open(log, O_WRONLY | O_CREAT | O_TRUNC);
        if (fd >= 0) { dup2(fd, 1); dup2(fd, 2); close(fd); }
        int nul = open("/dev/null", O_RDONLY);
        if (nul >= 0) { dup2(nul, 0); close(nul); }
        setvbuf(stdout, NULL, _IOLBF, 0);
        _exit(holder(pids, n, timeout_ms, fds[1]));
    }
    close(fds[1]);
    char ok = '0';
    if (read(fds[0], &ok, 1) != 1) ok = '0';
    close(fds[0]);
    cat_file(log, 0);
    if (ok != '1') {
        waitpid(child, NULL, 0);
        return -1;
    }
    printf("suspended; holder pid=%d (log: " STATE_DIR "/%s)\n", child, log);
    return 0;
}

static int signal_holder(const int *pids, int sig, unsigned timeout_s);

static int resume(const int *pids, int n, unsigned timeout_s)
{
    char hp[256];
    state_path(hp, sizeof(hp), pids[0], "holder");
    if (state_exists(hp))
        return signal_holder(pids, SIGUSR1, timeout_s);
    return resume_unheld(pids, n);
}

static int signal_holder(const int *pids, int sig, unsigned timeout_s)
{
    char path[256], result[256], log[256];
    state_path(path, sizeof(path), pids[0], "holder");
    state_path(result, sizeof(result), pids[0], "result");
    state_path(log, sizeof(log), pids[0], "log");

    FILE *fp = state_fopen(path);
    if (!fp) { fprintf(stderr, "pid=%d is not suspended (no " STATE_DIR "/%s)\n", pids[0], path); return -1; }
    int holder_pid = 0;
    if (fscanf(fp, "%d", &holder_pid) != 1) holder_pid = 0;
    fclose(fp);
    if (holder_pid <= 0 || kill(holder_pid, 0) != 0) {
        fprintf(stderr, "holder for pid=%d is gone; pids are no longer frozen\n", pids[0]);
        state_unlink(path);
        return -1;
    }

    struct stat st;
    long log_off = state_dir() >= 0 && fstatat(state_dir(), log, &st, AT_SYMLINK_NOFOLLOW) == 0 ? (long)st.st_size : 0;
    state_unlink(result);
    if (kill(holder_pid, sig) != 0) { perror("kill holder"); return -1; }
    for (unsigned waited = 0; !state_exists(result); waited++) {
        if (waited >= timeout_s * 10) { fprintf(stderr, "holder timed out\n"); return -1; }
        usleep(100000);
    }
    char buf[16] = "";
    fp = state_fopen(result);
    if (fp) { if (!fgets(buf, sizeof(buf), fp)) buf[0] = 0; fclose(fp); }
    cat_file(log, log_off);
    state_unlink(result);
    if (strncmp(buf, "ok", 2) != 0) {
        fprintf(stderr, "holder reported failure; pids stay frozen + CHECKPOINTED (retry)\n");
        return -1;
    }
    return 0;
}

static int parse_pid(const char *s, int *out)
{
    char *end;
    errno = 0;
    long v = strtol(s, &end, 10);
    if (errno || *end || v <= 0 || v > 0x7fffffff) return -1;
    *out = (int)v;
    return 0;
}

/* ── Chunk cache (see --cache) ────────────────────────────────────────── */

#define CHUNK_MAX (64UL << 20)  /* libnvsnap_gpushare's STAGE_SIZE */

static int cmp_str(const void *a, const void *b) { return strcmp(*(char *const *)a, *(char *const *)b); }

struct prefetch { const char *store, *cache; char **hashes; int n, next; size_t copied, cached, failed; pthread_mutex_t mu; };

static void chunk_file(const char *root, const char *hash, char *out, size_t n)
{
    snprintf(out, n, "%s/chunks/%.2s/%s", root, hash, hash);
}

static int open_direct(const char *path, int flags)
{
    int fd = open(path, flags | O_DIRECT | O_CLOEXEC, 0644);
    return fd >= 0 || errno == EEXIST ? fd : open(path, flags | O_CLOEXEC, 0644);
}

/* Read up to len bytes (wr: write len bytes) of buf; returns the count.
 * Some filesystems accept O_DIRECT at open and fail the I/O with EINVAL:
 * carry on buffered. */
static ssize_t full_io(int fd, int wr, char *buf, size_t len)
{
    size_t done = 0;
    while (done < len) {
        ssize_t k = wr ? write(fd, buf + done, len - done) : read(fd, buf + done, len - done);
        int fl;
        if (k < 0 && errno == EINVAL && (fl = fcntl(fd, F_GETFL)) >= 0 && (fl & O_DIRECT) &&
            fcntl(fd, F_SETFL, fl & ~O_DIRECT) == 0) continue;
        if (k < 0 && errno == EINTR) continue;
        if (k < 0) return -1;
        if (k == 0) break;
        done += k;
    }
    return done;
}

/* Create a temp file next to dst, under a random name: writers in other
 * pods (same pids, other pid namespaces) share the cache. */
static int open_tmp(const char *dst, char *tmp, size_t n, int direct)
{
    for (int i = 0; i < 8; i++) {
        unsigned long long r;
        if (getrandom(&r, sizeof(r), 0) != sizeof(r)) return -1;
        snprintf(tmp, n, "%s.%016llx.tmp", dst, r);
        int fd = direct ? open_direct(tmp, O_WRONLY | O_CREAT | O_EXCL)
                        : open(tmp, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0644);
        if (fd >= 0 || errno != EEXIST) return fd;
    }
    return -1;
}

static void *prefetch_thread(void *arg)
{
    struct prefetch *p = arg;
    void *buf;
    if (posix_memalign(&buf, 4096, CHUNK_MAX)) return NULL;
    for (;;) {
        pthread_mutex_lock(&p->mu);
        int i = p->next++;
        pthread_mutex_unlock(&p->mu);
        if (i >= p->n) break;
        char src[1024], dst[1024], tmp[1100];
        chunk_file(p->cache, p->hashes[i], dst, sizeof(dst));
        if (access(dst, F_OK) == 0) { __atomic_add_fetch(&p->cached, 1, __ATOMIC_RELAXED); continue; }
        chunk_file(p->store, p->hashes[i], src, sizeof(src));
        int in = open_direct(src, O_RDONLY);
        ssize_t len = in < 0 ? -1 : full_io(in, 0, buf, CHUNK_MAX);
        if (in >= 0) close(in);
        *strrchr(dst, '/') = 0;  /* <cache>/chunks/xx: create both levels */
        *strrchr(dst, '/') = 0;
        mkdir(dst, 0755);
        dst[strlen(dst)] = '/';
        mkdir(dst, 0755);
        dst[strlen(dst)] = '/';
        int out = len > 0 ? open_tmp(dst, tmp, sizeof(tmp), len % 4096 == 0) : -1;
        ssize_t w = out >= 0 ? full_io(out, 1, buf, len) : 0;
        /* synced before the rename: a crash never leaves a torn chunk under its name */
        if (out >= 0 && w == len && fdatasync(out) != 0) w = -1;
        if (out >= 0) close(out);
        if (len <= 0 || out < 0 || w < len || rename(tmp, dst) != 0) {
            if (out >= 0) unlink(tmp);
            fprintf(stderr, "prefetch %s failed\n", p->hashes[i]);
            __atomic_add_fetch(&p->failed, 1, __ATOMIC_RELAXED);
            continue;
        }
        __atomic_add_fetch(&p->copied, (size_t)len, __ATOMIC_RELAXED);
    }
    free(buf);
    return NULL;
}

/* Copy every chunk of checkpoint ckpt (its gpu-*.chunks lists) from store
 * into cache, with 16 threads. */
static int cache_prefetch(const char *store, const char *ckpt, const char *cache)
{
    struct prefetch p = { .store = store, .cache = cache, .mu = PTHREAD_MUTEX_INITIALIZER };
    int cap = 0;
    DIR *d = opendir(ckpt);
    if (!d) { fprintf(stderr, "%s: %s\n", ckpt, strerror(errno)); return -1; }
    struct dirent *e;
    while ((e = readdir(d))) {
        size_t l = strlen(e->d_name);
        if (l < 7 || strcmp(e->d_name + l - 7, ".chunks")) continue;
        char path[1024], line[128];
        snprintf(path, sizeof(path), "%s/%s", ckpt, e->d_name);
        FILE *fp = fopen(path, "r");
        while (fp && fgets(line, sizeof(line), fp)) {
            line[strcspn(line, "\n")] = 0;
            if (strlen(line) != 32) continue;
            if (p.n == cap) p.hashes = realloc(p.hashes, (cap = cap ? 2 * cap : 1024) * sizeof(char *));
            p.hashes[p.n++] = strdup(line);
        }
        if (fp) fclose(fp);
    }
    closedir(d);
    qsort(p.hashes, p.n, sizeof(char *), cmp_str);
    int u = 0;
    for (int i = 0; i < p.n; i++) if (!u || strcmp(p.hashes[i], p.hashes[u - 1])) p.hashes[u++] = p.hashes[i];
    p.n = u;
    struct timespec t0, t1;
    clock_gettime(CLOCK_MONOTONIC, &t0);
    pthread_t t[16];
    for (int i = 0; i < 16; i++) pthread_create(&t[i], NULL, prefetch_thread, &p);
    for (int i = 0; i < 16; i++) pthread_join(t[i], NULL);
    clock_gettime(CLOCK_MONOTONIC, &t1);
    double secs = (t1.tv_sec - t0.tv_sec) + (t1.tv_nsec - t0.tv_nsec) / 1e9;
    printf("prefetched %zu MiB of %d chunk(s) in %.1fs (%.1f GB/s), %zu already cached, %zu failed\n",
           p.copied >> 20, p.n, secs, p.copied / secs / 1e9, p.cached, p.failed);
    return p.failed ? -1 : 0;
}

struct centry { time_t mtime; off_t size; char *path; };

static int cmp_mtime(const void *a, const void *b)
{
    const struct centry *x = a, *y = b;
    return x->mtime < y->mtime ? -1 : x->mtime > y->mtime;
}

/* Evict least recently used chunks (mtime: libnvsnap_gpushare touches a chunk
 * on every cache hit) once the cache exceeds max bytes, down to 80% of it;
 * chunks used in the last 10 minutes stay (a restore may be reading them).
 * Leftover temp files older than an hour go too. */
static int cache_gc(const char *cache, unsigned long long max)
{
    char top[1024];
    snprintf(top, sizeof(top), "%s/chunks", cache);
    DIR *d = opendir(top);
    if (!d) { fprintf(stderr, "%s: %s\n", top, strerror(errno)); return -1; }
    struct centry *v = NULL;
    int n = 0, cap = 0, tmps = 0;
    unsigned long long total = 0;
    time_t now = time(NULL);
    struct dirent *e;
    while ((e = readdir(d))) {
        if (e->d_name[0] == '.') continue;
        char sub[1300];
        snprintf(sub, sizeof(sub), "%s/%s", top, e->d_name);
        DIR *sd = opendir(sub);
        struct dirent *f;
        while (sd && (f = readdir(sd))) {
            if (f->d_name[0] == '.') continue;
            char path[1600];
            struct stat st;
            snprintf(path, sizeof(path), "%s/%s", sub, f->d_name);
            if (stat(path, &st) != 0) continue;
            if (strstr(f->d_name, ".tmp")) {
                if (now - st.st_mtime > 3600 && unlink(path) == 0) tmps++;
                continue;
            }
            if (n == cap) v = realloc(v, (cap = cap ? 2 * cap : 1024) * sizeof(*v));
            v[n++] = (struct centry){ st.st_mtime, st.st_size, strdup(path) };
            total += st.st_size;
        }
        if (sd) closedir(sd);
    }
    closedir(d);
    unsigned long long freed = 0, before = total;
    int gone = 0;
    if (total > max) {
        qsort(v, n, sizeof(*v), cmp_mtime);
        for (int i = 0; i < n && total > max / 10 * 8; i++) {
            if (now - v[i].mtime < 600) break;  /* sorted: the rest are newer */
            if (unlink(v[i].path) == 0) { total -= v[i].size; freed += v[i].size; gone++; }
        }
    }
    printf("cache %s: %d chunk(s), %llu GiB -> %llu GiB (limit %llu GiB): evicted %d chunk(s), %llu GiB; "
           "removed %d stale temp file(s)\n", cache, n, before >> 30, total >> 30, max >> 30, gone, freed >> 30, tmps);
    for (int i = 0; i < n; i++) free(v[i].path);
    free(v);
    return 0;
}

static void usage(const char *prog)
{
    fprintf(stderr,
        "Usage: %s [--timeout-ms N] <action> <pid> [pid ...]\n"
        "Actions: state, lock, checkpoint, restore, unlock\n"
        "         full-save    (lock all, then checkpoint all; rollback on failure)\n"
        "         full-restore (restore all, then unlock all)\n"
        "         suspend      (full-save + freeze CPU threads until resume)\n"
        "         stop         (after suspend: leave pids SIGSTOPped for CRIU dump)\n"
        "         resume       (restore + unlock all, then thaw)\n"
        "         gpus [FILE]  (print or write the visible GPU UUIDs, for --gpu-map)\n"
        "--timeout-ms N: lock timeout per pid (default 10000, 0 = wait forever)\n"
        "--gpu-map FILE: restore GPUs listed in FILE onto the GPUs visible here\n"
        "--store S --ckpt-dir C: (suspend) libnvsnap_gpushare processes save GPU memory\n"
        "         to chunk store S; C lists the chunks of this checkpoint\n"
        "--cache DIR: (suspend, resume) node-local cache of the chunk store\n"
        "--fabric-map DIR: (suspend, resume) multi-node NVLink: after load, write the new\n"
        "         fabric handles to DIR/out and wait for DIR/in, all nodes' out merged\n"
        "Cache:   cache-prefetch STORE CKPT-DIR CACHE | cache-gc CACHE MAX-GiB\n"
        "Multi-GPU: pass all ranks in one call, e.g.:\n"
        "  %s full-save 503 504 505 508\n", prog, prog);
}

int main(int argc, char **argv)
{
    unsigned timeout_ms = 10000;
    int argi = 1;

    setvbuf(stdout, NULL, _IOLBF, 0); /* keep stdout/stderr ordered in logs */

    while (argi + 1 < argc && strncmp(argv[argi], "--", 2) == 0) {
        if (strcmp(argv[argi], "--timeout-ms") == 0)
            timeout_ms = (unsigned)strtoul(argv[argi + 1], NULL, 10);
        else if (strcmp(argv[argi], "--gpu-map") == 0)
            gpu_map_file = argv[argi + 1];
        else if (strcmp(argv[argi], "--store") == 0)
            store_dir = argv[argi + 1];
        else if (strcmp(argv[argi], "--ckpt-dir") == 0)
            ckpt_dir = argv[argi + 1];
        else if (strcmp(argv[argi], "--cache") == 0)
            cache_dir = argv[argi + 1];
        else if (strcmp(argv[argi], "--fabric-map") == 0)
            fabric_dir = argv[argi + 1];
        else
            break;
        argi += 2;
    }
    if (!store_dir != !ckpt_dir) {
        fprintf(stderr, "--store and --ckpt-dir go together\n");
        return 1;
    }

    if (argc - argi == 4 && strcmp(argv[argi], "cache-prefetch") == 0)
        return cache_prefetch(argv[argi + 1], argv[argi + 2], argv[argi + 3]) ? 1 : 0;
    if (argc - argi == 3 && strcmp(argv[argi], "cache-gc") == 0)
        return cache_gc(argv[argi + 1], strtoull(argv[argi + 2], NULL, 10) << 30) ? 1 : 0;
    if (argi < argc && strcmp(argv[argi], "gpus") == 0) {
        /* gpus [FILE]: the caller can keep the map out of the workload's filesystem. */
        CUuuid u[MAX_GPUS];
        char str[41];
        FILE *out = argc - argi > 1 ? fopen(argv[argi + 1], "w") : stdout;
        if (!out) { fprintf(stderr, "%s: %s\n", argv[argi + 1], strerror(errno)); return 1; }
        if (load_api() < 0) return 1;
        int n = visible_gpus(u, MAX_GPUS);
        if (n < 0) { fprintf(stderr, "cannot query GPUs\n"); return 1; }
        for (int i = 0; i < n; i++) { uuid_str(&u[i], str); fprintf(out, "%s\n", str); }
        return fclose(out) == 0 ? 0 : 1;
    }
    if (argc - argi < 2) { usage(argv[0]); return 1; }

    const char *action = argv[argi++];
    int n = argc - argi;
    int *pids = malloc(n * sizeof(int));
    for (int i = 0; i < n; i++) {
        if (parse_pid(argv[argi + i], &pids[i]) < 0) {
            fprintf(stderr, "Invalid pid: %s\n", argv[argi + i]);
            free(pids);
            return 1;
        }
    }

    int failures = 0;

    /* suspend forks and resume may only signal: no CUDA init here. */
    if (strcmp(action, "suspend") == 0) {
        failures = suspend(pids, n, timeout_ms) < 0;
        free(pids);
        return failures;
    }
    if (strcmp(action, "resume") == 0) {
        failures = resume(pids, n, 600) < 0;
        free(pids);
        return failures;
    }
    if (strcmp(action, "stop") == 0) {
        failures = signal_holder(pids, SIGUSR2, 60) < 0;
        free(pids);
        return failures;
    }

    if (load_api() < 0) { free(pids); return 1; }

    if (strcmp(action, "full-save") == 0) {
        failures = full_save(pids, n, timeout_ms) < 0;
    } else if (strcmp(action, "full-restore") == 0) {
        failures = full_restore(pids, n) < 0;
    } else if (strcmp(action, "lock") == 0) {
        failures = lock_all(pids, n, timeout_ms) < 0;
    } else if (strcmp(action, "state") == 0 || strcmp(action, "checkpoint") == 0 ||
               strcmp(action, "restore") == 0 || strcmp(action, "unlock") == 0) {
        for (int i = 0; i < n; i++) {
            int r = 0;
            if (action[0] == 's') {
                CUprocessState s;
                r = get_state(pids[i], &s);
                if (r == 0) printf("pid=%d state=%s\n", pids[i], state_name(s));
            } else if (action[0] == 'c') {
                r = do_checkpoint(pids[i]);
            } else if (action[0] == 'r') {
                r = do_restore(pids[i]);
            } else {
                r = do_unlock(pids[i]);
            }
            if (r < 0) failures++;
        }
    } else {
        fprintf(stderr, "Unknown action: %s\n", action);
        usage(argv[0]);
        free(pids);
        return 1;
    }

    free(pids);
    return failures > 0 ? 1 : 0;
}

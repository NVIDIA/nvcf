/*
 * nvsnap-cuda-checkpoint: a drop-in replacement for NVIDIA's `cuda-checkpoint`
 * binary, built directly on the CUDA driver checkpoint API (the CUDA_CHECKPOINT
 * group: cuCheckpointProcess{GetState,GetRestoreThreadId,Lock,Checkpoint,
 * Restore,Unlock}). Requires driver 570+ (550+ for basic actions).
 *
 * It mirrors the upstream CLI so CRIU's cuda_plugin (which execs `cuda-checkpoint`
 * by name on PATH) can call this instead, with no other changes:
 *
 *   --get-state       --pid <pid>
 *   --action lock|checkpoint|restore|unlock|resume --pid <pid> [--timeout <ms>]
 *   --toggle          --pid <pid>
 *   --get-restore-tid --pid <pid>
 *
 * Plus one operation upstream's CLI does not expose:
 *
 *   --gpu-map <old>:<new>[,<old>:<new>...]
 *
 * GPU migration (driver r580+): restore a checkpoint onto different physical
 * GPUs than it was captured on, by mapping each source device UUID to a target
 * device UUID. This is what lets a restore be scheduled wherever there is
 * capacity instead of being pinned to the GPU slot it was captured from.
 * Applies to restore, resume, and the restore half of toggle; ignored (with a
 * diagnostic) on lock/checkpoint, which have no such argument.
 *
 * Requires CUDA 13 headers: 12.x declares CUcheckpointRestoreArgs as an opaque
 * reserved[8] and has no gpuPairs member to populate.
 *
 * Build (in an env with cuda.h and the driver's libcuda.so):
 *   gcc -O2 nvsnap-cuda-checkpoint.c -o nvsnap-cuda-checkpoint -lcuda
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <getopt.h>
#include <cuda.h>

/* Match NVIDIA cuda-checkpoint exactly: error text -> stderr, exit code 1,
 * success values -> stdout. The CUDA error string is the human-readable form
 * from cuGetErrorString (e.g. "initialization error"). */
static const char *cu_str(CUresult r)
{
    const char *s = NULL;
    cuGetErrorString(r, &s);
    return s ? s : "unknown error";
}

static int err_action(const char *verb, int pid, CUresult r)
{
    fprintf(stderr, "Could not %s on process ID %d: \"%s\"\n", verb, pid, cu_str(r));
    return 1;
}

static const char *state_name(CUprocessState s)
{
    switch (s) {
    case CU_PROCESS_STATE_RUNNING:      return "running";
    case CU_PROCESS_STATE_LOCKED:       return "locked";
    case CU_PROCESS_STATE_CHECKPOINTED: return "checkpointed";
    case CU_PROCESS_STATE_FAILED:       return "failed";
    default:                            return "unknown";
    }
}

/* ---- multi-pid batching --------------------------------------------------
 *
 * The CRIU plugin drives this one pid per exec, so an N-rank job means N
 * separate processes with N driver attaches and, more importantly, N gaps
 * between them. On a multi-GPU job those gaps are where collective traffic
 * resumes between one rank being locked and the next, which is the shape of
 * the documented "hangs on 2nd rank (lock timeout)" failure.
 *
 * Accepting several pids in one invocation removes the gaps and lets the
 * ordering be stated explicitly (see the "save" action). A single --pid still
 * behaves exactly as before, so the plugin's calls are unaffected.
 */
#define MAX_PIDS 512
static int g_pids[MAX_PIDS];
static unsigned int g_pid_count;

static int add_pids(const char *spec)
{
    char *dup = strdup(spec);
    if (!dup) {
        fprintf(stderr, "error: out of memory\n");
        return -1;
    }
    int rc = 0;
    char *save = NULL;
    for (char *tok = strtok_r(dup, ",", &save); tok; tok = strtok_r(NULL, ",", &save)) {
        int pid = atoi(tok);
        if (pid <= 0) {
            fprintf(stderr, "error: invalid pid '%s'\n", tok);
            rc = -1;
            break;
        }
        if (g_pid_count >= MAX_PIDS) {
            fprintf(stderr, "error: more than %d pids\n", MAX_PIDS);
            rc = -1;
            break;
        }
        g_pids[g_pid_count++] = pid;
    }
    free(dup);
    return rc;
}

/* ---- GPU migration (--gpu-map) ------------------------------------------
 *
 * The driver identifies devices by UUID, not by index: an index is only
 * meaningful relative to one process's CUDA_VISIBLE_DEVICES on one node, and
 * the whole point of migration is that the target node's enumeration differs.
 * Indices are still accepted as a convenience for same-node testing and are
 * resolved to UUIDs here, before they can be misread anywhere else.
 */
static CUcheckpointGpuPair *g_pairs;
static unsigned int g_pairs_count;

static int hex_nibble(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static int uuid_from_index(int idx, CUuuid *out)
{
    CUdevice dev;
    CUresult r = cuDeviceGet(&dev, idx);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "--gpu-map: no device at index %d: \"%s\"\n", idx, cu_str(r));
        return -1;
    }
    r = cuDeviceGetUuid(out, dev);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "--gpu-map: cannot read UUID of device %d: \"%s\"\n", idx, cu_str(r));
        return -1;
    }
    return 0;
}

/* Accepts a decimal device index, or a UUID as 32 hex digits with an optional
 * "GPU-" prefix and optional dashes -- the forms nvidia-smi and the k8s device
 * plugin emit, so an operator can paste either without reformatting. */
static int parse_gpu_id(const char *s, CUuuid *out)
{
    if (!s || !*s) {
        fprintf(stderr, "--gpu-map: empty device id\n");
        return -1;
    }

    const char *p = s;
    int all_digits = 1;
    for (const char *q = s; *q; q++) {
        if (!isdigit((unsigned char)*q)) { all_digits = 0; break; }
    }
    if (all_digits)
        return uuid_from_index(atoi(s), out);

    if (!strncmp(p, "GPU-", 4) || !strncmp(p, "gpu-", 4))
        p += 4;

    int n = 0;
    for (; *p && n < 16; p++) {
        if (*p == '-') continue;
        int hi = hex_nibble(*p);
        int lo = (p[1] && p[1] != '-') ? hex_nibble(p[1]) : -1;
        if (hi < 0 || lo < 0) {
            fprintf(stderr, "--gpu-map: malformed device id '%s'\n", s);
            return -1;
        }
        out->bytes[n++] = (char)((hi << 4) | lo);
        p++;
    }
    if (n != 16) {
        fprintf(stderr, "--gpu-map: device id '%s' is not 32 hex digits\n", s);
        return -1;
    }
    return 0;
}

/* spec: "old:new[,old:new...]". Every GPU visible to the target process must
 * appear, including ones it never touched -- the driver rejects a partial map
 * rather than leaving unlisted devices alone, so a short map fails at restore
 * with a considerably less obvious error than this one. */
static int parse_gpu_map(const char *spec)
{
    unsigned int cap = 1;
    for (const char *q = spec; *q; q++)
        if (*q == ',') cap++;

    g_pairs = calloc(cap, sizeof(*g_pairs));
    if (!g_pairs) {
        fprintf(stderr, "--gpu-map: out of memory\n");
        return -1;
    }

    char *dup = strdup(spec);
    if (!dup) {
        fprintf(stderr, "--gpu-map: out of memory\n");
        return -1;
    }

    int rc = 0;
    char *save = NULL;
    for (char *tok = strtok_r(dup, ",", &save); tok; tok = strtok_r(NULL, ",", &save)) {
        char *colon = strchr(tok, ':');
        if (!colon) {
            fprintf(stderr, "--gpu-map: expected <old>:<new>, got '%s'\n", tok);
            rc = -1;
            break;
        }
        *colon = '\0';
        if (parse_gpu_id(tok, &g_pairs[g_pairs_count].oldUuid) < 0 ||
            parse_gpu_id(colon + 1, &g_pairs[g_pairs_count].newUuid) < 0) {
            rc = -1;
            break;
        }
        g_pairs_count++;
    }
    free(dup);
    if (rc < 0)
        return rc;

    int visible = 0;
    if (cuDeviceGetCount(&visible) == CUDA_SUCCESS && (unsigned int)visible != g_pairs_count) {
        fprintf(stderr,
                "--gpu-map: %u pair(s) given but %d GPU(s) are visible; every visible "
                "GPU must be mapped (use i:i for the ones that do not move)\n",
                g_pairs_count, visible);
        return -1;
    }
    return 0;
}

static int do_lock(int pid, unsigned int timeout_ms)
{
    CUcheckpointLockArgs a = {0};
    a.timeoutMs = timeout_ms;
    CUresult r = cuCheckpointProcessLock(pid, &a);
    return r == CUDA_SUCCESS ? 0 : err_action("lock", pid, r);
}

static int do_checkpoint(int pid)
{
    CUcheckpointCheckpointArgs a = {0};
    CUresult r = cuCheckpointProcessCheckpoint(pid, &a);
    return r == CUDA_SUCCESS ? 0 : err_action("checkpoint", pid, r);
}

static int do_restore(int pid)
{
    CUcheckpointRestoreArgs a = {0};
    /* Left zeroed when no map was given, which is the non-migrating restore
     * the driver has always done -- the field is additive, not a mode switch. */
    if (g_pairs_count) {
        a.gpuPairs = g_pairs;
        a.gpuPairsCount = g_pairs_count;
    }
    CUresult r = cuCheckpointProcessRestore(pid, &a);
    return r == CUDA_SUCCESS ? 0 : err_action("restore", pid, r);
}

static int do_unlock(int pid)
{
    CUcheckpointUnlockArgs a = {0};
    CUresult r = cuCheckpointProcessUnlock(pid, &a);
    return r == CUDA_SUCCESS ? 0 : err_action("unlock", pid, r);
}

/* resume: restore then unlock in one process, so cuInit (the ~2.7s driver
 * attach) is paid once instead of once per action. Used by the CRIU cuda
 * plugin on the common restore path (process was running at checkpoint). */
static int do_resume(int pid)
{
    int rc = do_restore(pid);
    return rc ? rc : do_unlock(pid);
}

static int do_get_state(int pid)
{
    CUprocessState s;
    CUresult r = cuCheckpointProcessGetState(pid, &s);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "Error getting process state for process ID %d: \"%s\"\n", pid, cu_str(r));
        return 1;
    }
    printf("%s\n", state_name(s));
    return 0;
}

static int do_get_restore_tid(int pid)
{
    int tid = 0;
    CUresult r = cuCheckpointProcessGetRestoreThreadId(pid, &tid);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "Could not find restore thread for process ID %d\n", pid);
        return 1;
    }
    printf("%d\n", tid);
    return 0;
}

/* save: lock EVERY pid, then checkpoint every pid, from this one process.
 *
 * The two-phase order is the point, not an implementation detail. Locking rank
 * 0 while rank 1 is still running leaves rank 1 free to post a collective that
 * rank 0 will never service, and rank 1's own lock then waits on an operation
 * that cannot complete. Locking all ranks first makes that window empty.
 *
 * On any lock failure every already-locked pid is unlocked again, so a failed
 * attempt leaves the job running rather than wedged half-locked.
 */
static int do_save_all(unsigned int timeout_ms)
{
    unsigned int locked = 0;
    for (; locked < g_pid_count; locked++) {
        if (do_lock(g_pids[locked], timeout_ms) != 0) {
            fprintf(stderr, "save: lock failed on pid %d (%u/%u locked); rolling back\n",
                    g_pids[locked], locked, g_pid_count);
            for (unsigned int j = 0; j < locked; j++)
                (void)do_unlock(g_pids[j]);
            return 1;
        }
    }
    for (unsigned int i = 0; i < g_pid_count; i++) {
        if (do_checkpoint(g_pids[i]) != 0) {
            fprintf(stderr, "save: checkpoint failed on pid %d (%u/%u checkpointed)\n",
                    g_pids[i], i, g_pid_count);
            return 1;
        }
    }
    return 0;
}

/* toggle: running -> (lock, checkpoint); checkpointed -> (restore, unlock) */
static int do_toggle(int pid, unsigned int timeout_ms)
{
    CUprocessState s;
    CUresult r = cuCheckpointProcessGetState(pid, &s);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "Error getting process state for process ID %d: \"%s\"\n", pid, cu_str(r));
        return 1;
    }

    if (s == CU_PROCESS_STATE_RUNNING) {
        int rc = do_lock(pid, timeout_ms);
        return rc ? rc : do_checkpoint(pid);
    } else if (s == CU_PROCESS_STATE_CHECKPOINTED) {
        int rc = do_restore(pid);
        return rc ? rc : do_unlock(pid);
    }
    fprintf(stderr, "toggle: process in state '%s', expected running or checkpointed\n", state_name(s));
    return 1;
}

static void usage(const char *p)
{
    fprintf(stderr,
        "nvsnap-cuda-checkpoint: CUDA checkpoint/restore via the driver API.\n"
        "Operations:\n"
        "  --get-state       --pid <pid>\n"
        "  --action lock|checkpoint|restore|unlock|resume --pid <pid> [--timeout <ms>]\n"
        "  --action save     --pid <pid> --pid <pid> ...  (lock all, then checkpoint all)\n"
        "  --toggle          --pid <pid>\n"
        "  --get-restore-tid --pid <pid>\n"
        "Options:\n"
        "  --pid|-p <pid>        target pid; repeatable, or comma-separated.\n"
        "                        Several pids are driven from this one process,\n"
        "                        which is what 'save' needs to lock every rank\n"
        "                        before any of them is checkpointed.\n"
        "  --timeout|-t <ms>     lock timeout in milliseconds (0 = no timeout)\n"
        "  --gpu-map <spec>      GPU migration on restore (driver r580+).\n"
        "                        <spec> = <old>:<new>[,<old>:<new>...]\n"
        "                        each side is a device index or a UUID\n"
        "                        (32 hex digits, optional GPU- prefix/dashes).\n"
        "                        Every visible GPU must appear; use i:i to pin.\n"
        "  --help|-h\n");
}

int main(int argc, char **argv)
{
    unsigned int timeout_ms = 0;
    const char *action = NULL;
    const char *gpu_map = NULL;
    int get_state = 0, toggle = 0, get_tid = 0;

    static struct option opts[] = {
        {"action",         required_argument, 0, 'a'},
        {"pid",            required_argument, 0, 'p'},
        {"timeout",        required_argument, 0, 't'},
        {"gpu-map",        required_argument, 0, 'm'},
        {"get-state",      no_argument,       0, 's'},
        {"toggle",         no_argument,       0, 'g'},
        {"get-restore-tid",no_argument,       0, 'r'},
        {"help",           no_argument,       0, 'h'},
        {0,0,0,0}
    };
    int c;
    while ((c = getopt_long(argc, argv, "a:p:t:m:sgrh", opts, NULL)) != -1) {
        switch (c) {
        case 'a': action = optarg; break;
        case 'p': if (add_pids(optarg) < 0) return 2; break;
        case 't': timeout_ms = (unsigned int)strtoul(optarg, NULL, 10); break;
        case 'm': gpu_map = optarg; break;
        case 's': get_state = 1; break;
        case 'g': toggle = 1; break;
        case 'r': get_tid = 1; break;
        case 'h': usage(argv[0]); return 0;
        default:  usage(argv[0]); return 2;
        }
    }

    if (g_pid_count == 0) {
        fprintf(stderr, "error: --pid <pid> is required\n");
        usage(argv[0]);
        return 2;
    }

    /* Operating on another process's GPU state needs only cuInit; do NOT retain
     * a context here (that would acquire a GPU in this helper). */
    CUresult r = cuInit(0);
    if (r != CUDA_SUCCESS) {
        fprintf(stderr, "cuInit failed: \"%s\"\n", cu_str(r));
        return 1;
    }

    /* Parsed after cuInit: index forms and the visible-GPU count both need the
     * driver up. Rejecting a bad map here, before any state transition, keeps a
     * typo from leaving the target process locked or half-restored. */
    if (gpu_map && parse_gpu_map(gpu_map) < 0)
        return 2;

    /* One pid: identical to before, including exit codes, so the CRIU plugin
     * is unaffected. Several: apply in the given order and fail on the first
     * error, except for "save" which owns its own ordering and rollback. */
    if (get_state) {
        for (unsigned int i = 0; i < g_pid_count; i++)
            if (do_get_state(g_pids[i]) != 0) return 1;
        return 0;
    }
    if (get_tid) {
        for (unsigned int i = 0; i < g_pid_count; i++)
            if (do_get_restore_tid(g_pids[i]) != 0) return 1;
        return 0;
    }
    if (toggle) {
        for (unsigned int i = 0; i < g_pid_count; i++)
            if (do_toggle(g_pids[i], timeout_ms) != 0) return 1;
        return 0;
    }

    if (!action) {
        fprintf(stderr, "error: one of --action/--get-state/--toggle/--get-restore-tid required\n");
        usage(argv[0]);
        return 2;
    }
    /* Say so rather than silently ignoring it: a map on the capture half is
     * almost always someone expecting migration to be chosen at checkpoint
     * time, and finding out at restore is far more expensive. */
    if (g_pairs_count && (!strcmp(action, "lock") || !strcmp(action, "checkpoint")))
        fprintf(stderr, "warning: --gpu-map has no effect on '%s'; it applies at restore\n", action);

    if (!strcmp(action, "save"))       return do_save_all(timeout_ms);

    for (unsigned int i = 0; i < g_pid_count; i++) {
        int rc;
        if      (!strcmp(action, "lock"))       rc = do_lock(g_pids[i], timeout_ms);
        else if (!strcmp(action, "checkpoint")) rc = do_checkpoint(g_pids[i]);
        else if (!strcmp(action, "restore"))    rc = do_restore(g_pids[i]);
        else if (!strcmp(action, "unlock"))     rc = do_unlock(g_pids[i]);
        else if (!strcmp(action, "resume"))     rc = do_resume(g_pids[i]);
        else break;
        if (rc) return rc;
        if (i + 1 == g_pid_count) return 0;
    }

    fprintf(stderr, "error: unknown action '%s'\n", action);
    usage(argv[0]);
    return 2;
}

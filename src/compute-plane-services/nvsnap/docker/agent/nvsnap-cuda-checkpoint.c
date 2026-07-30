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
 * Single-GPU scope: the 12.x CUcheckpointRestoreArgs has no gpuPairs field, so
 * no GPU migration (that is a CUDA 13 / r580 feature for the multi-GPU phase).
 *
 * Build (in an env with cuda.h and the driver's libcuda.so):
 *   gcc -O2 nvsnap-cuda-checkpoint.c -o nvsnap-cuda-checkpoint -lcuda
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
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
        "  --toggle          --pid <pid>\n"
        "  --get-restore-tid --pid <pid>\n"
        "Options:\n"
        "  --pid|-p <pid>        target pid\n"
        "  --timeout|-t <ms>     lock timeout in milliseconds (0 = no timeout)\n"
        "  --help|-h\n");
}

int main(int argc, char **argv)
{
    int pid = -1;
    unsigned int timeout_ms = 0;
    const char *action = NULL;
    int get_state = 0, toggle = 0, get_tid = 0;

    static struct option opts[] = {
        {"action",         required_argument, 0, 'a'},
        {"pid",            required_argument, 0, 'p'},
        {"timeout",        required_argument, 0, 't'},
        {"get-state",      no_argument,       0, 's'},
        {"toggle",         no_argument,       0, 'g'},
        {"get-restore-tid",no_argument,       0, 'r'},
        {"help",           no_argument,       0, 'h'},
        {0,0,0,0}
    };
    int c;
    while ((c = getopt_long(argc, argv, "a:p:t:sgrh", opts, NULL)) != -1) {
        switch (c) {
        case 'a': action = optarg; break;
        case 'p': pid = atoi(optarg); break;
        case 't': timeout_ms = (unsigned int)strtoul(optarg, NULL, 10); break;
        case 's': get_state = 1; break;
        case 'g': toggle = 1; break;
        case 'r': get_tid = 1; break;
        case 'h': usage(argv[0]); return 0;
        default:  usage(argv[0]); return 2;
        }
    }

    if (pid <= 0) {
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

    if (get_state)  return do_get_state(pid);
    if (get_tid)    return do_get_restore_tid(pid);
    if (toggle)     return do_toggle(pid, timeout_ms);

    if (!action) {
        fprintf(stderr, "error: one of --action/--get-state/--toggle/--get-restore-tid required\n");
        usage(argv[0]);
        return 2;
    }
    if (!strcmp(action, "lock"))       return do_lock(pid, timeout_ms);
    if (!strcmp(action, "checkpoint")) return do_checkpoint(pid);
    if (!strcmp(action, "restore"))    return do_restore(pid);
    if (!strcmp(action, "unlock"))     return do_unlock(pid);
    if (!strcmp(action, "resume"))     return do_resume(pid);

    fprintf(stderr, "error: unknown action '%s'\n", action);
    usage(argv[0]);
    return 2;
}

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * nvsnap-gpu-restore — GPU checkpoint/restore via CUDA Checkpoint API.
 *
 * Uses the official cuCheckpointProcess* functions from the CUDA Driver API.
 * No ptrace, no ioctl replay, no cuda-checkpoint CLI.
 *
 * Usage:
 *   nvsnap-gpu-restore lock <pid>          Lock (pause CUDA)
 *   nvsnap-gpu-restore checkpoint <pid>    Save GPU→host (after lock)
 *   nvsnap-gpu-restore restore <pid>       Reload host→GPU (after CRIU)
 *   nvsnap-gpu-restore unlock <pid>        Resume CUDA
 *   nvsnap-gpu-restore state <pid>         Query state
 *   nvsnap-gpu-restore full-save <pid>     Lock + checkpoint
 *   nvsnap-gpu-restore full-restore <pid>  Restore + unlock
 *
 * For multi-GPU: call once per TP worker PID.
 *
 * Validated: 60 GB single GPU (18s checkpoint, 12s restore),
 *           2x GPU with NCCL (sequential, no hang).
 */
#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef int CUresult;

typedef CUresult (*cuInit_t)(unsigned);
typedef CUresult (*cuCheckpointLock_t)(int, void*);
typedef CUresult (*cuCheckpointCheckpoint_t)(int, void*);
typedef CUresult (*cuCheckpointRestore_t)(int, void*);
typedef CUresult (*cuCheckpointUnlock_t)(int, void*);
typedef CUresult (*cuCheckpointGetState_t)(int, int*);

static cuInit_t fn_init;
static cuCheckpointLock_t fn_lock;
static cuCheckpointCheckpoint_t fn_ckpt;
static cuCheckpointRestore_t fn_restore;
static cuCheckpointUnlock_t fn_unlock;
static cuCheckpointGetState_t fn_state;

static int load_api(void)
{
    void *h = dlopen("libcuda.so.1", RTLD_LAZY);
    if (!h) { fprintf(stderr, "Cannot load libcuda.so.1\n"); return -1; }

    fn_init = dlsym(h, "cuInit");
    fn_lock = dlsym(h, "cuCheckpointProcessLock");
    fn_ckpt = dlsym(h, "cuCheckpointProcessCheckpoint");
    fn_restore = dlsym(h, "cuCheckpointProcessRestore");
    fn_unlock = dlsym(h, "cuCheckpointProcessUnlock");
    fn_state = dlsym(h, "cuCheckpointProcessGetState");

    if (!fn_lock || !fn_ckpt || !fn_restore || !fn_unlock) {
        fprintf(stderr, "CUDA Checkpoint API not available (need driver 555+)\n");
        return -1;
    }

    if (fn_init) fn_init(0);
    return 0;
}

static const char *state_name(int s)
{
    switch (s) {
    case 0: return "RUNNING";
    case 1: return "LOCKED";
    case 2: return "CHECKPOINTED";
    default: return "UNKNOWN";
    }
}

int main(int argc, char **argv)
{
    if (argc < 3) {
        fprintf(stderr, "Usage: %s <action> <pid> [pid2 pid3 ...]\n", argv[0]);
        fprintf(stderr, "Actions: lock, checkpoint, restore, unlock, state\n");
        fprintf(stderr, "         full-save (lock+checkpoint)\n");
        fprintf(stderr, "         full-restore (restore+unlock)\n");
        fprintf(stderr, "Multiple PIDs: processes in order, e.g.:\n");
        fprintf(stderr, "  %s full-save 503 504 505 508\n", argv[0]);
        return 1;
    }

    const char *action = argv[1];
    int num_pids = argc - 2;
    int *pids = malloc(num_pids * sizeof(int));
    for (int i = 0; i < num_pids; i++)
        pids[i] = atoi(argv[i + 2]);

    if (load_api() < 0) return 1;

    int failures = 0;
    CUresult r;

    for (int i = 0; i < num_pids; i++) {
        int pid = pids[i];

        if (strcmp(action, "state") == 0) {
            int s = -1;
            r = fn_state(pid, &s);
            printf("pid=%d state=%s (%d) ret=%d\n", pid, state_name(s), s, r);

        } else if (strcmp(action, "lock") == 0) {
            r = fn_lock(pid, NULL);
            printf("pid=%d lock=%d\n", pid, r);
            if (r != 0) failures++;

        } else if (strcmp(action, "checkpoint") == 0) {
            r = fn_ckpt(pid, NULL);
            printf("pid=%d checkpoint=%d\n", pid, r);
            if (r != 0) failures++;

        } else if (strcmp(action, "restore") == 0) {
            r = fn_restore(pid, NULL);
            printf("pid=%d restore=%d\n", pid, r);
            if (r != 0) failures++;

        } else if (strcmp(action, "unlock") == 0) {
            r = fn_unlock(pid, NULL);
            printf("pid=%d unlock=%d\n", pid, r);
            if (r != 0) failures++;

        } else if (strcmp(action, "full-save") == 0) {
            r = fn_lock(pid, NULL);
            printf("pid=%d lock=%d", pid, r);
            if (r != 0) { printf(" FAILED\n"); failures++; continue; }

            r = fn_ckpt(pid, NULL);
            printf(" checkpoint=%d\n", r);
            if (r != 0) { fn_unlock(pid, NULL); failures++; }

        } else if (strcmp(action, "full-restore") == 0) {
            r = fn_restore(pid, NULL);
            printf("pid=%d restore=%d", pid, r);
            if (r != 0) { printf(" FAILED\n"); failures++; continue; }

            r = fn_unlock(pid, NULL);
            printf(" unlock=%d\n", r);

        } else {
            fprintf(stderr, "Unknown action: %s\n", action);
            free(pids);
            return 1;
        }
    }

    free(pids);
    return failures > 0 ? 1 : 0;
}

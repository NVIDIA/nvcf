/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Stay out of our own tooling.
 *
 * Workloads enable interception by writing /etc/ld.so.preload. The loader
 * honours that file for EVERY process that execs in the mount namespace, not
 * just the workload -- so the CRIU that nsenters in to dump the container is
 * force-loaded with this library too, as are the cuda-checkpoint and
 * iptables-restore helpers CRIU execs from the bundle.
 *
 * Inside CRIU that is fatal: the dump wedges with CRIU blocked forever in
 * wait4() before it finishes seizing the task tree. Reproduced with a plain
 * `sleep` victim and no GPU -- preloaded into the victim only, the dump
 * succeeds (813-line dump.log); preloaded into CRIU only, it hangs (36-line
 * dump.log, zero images). It is the load into CRIU that breaks, not the load
 * into the workload.
 *
 * No environment gate can undo this after the fact: our constructors run
 * before any NVSNAP_* variable is consulted, and NVSNAP_LIGHTWEIGHT=1,
 * NVSNAP_DISABLE_QUIESCE=1 and NVSNAP_LOG_LEVEL=0 were each measured to still
 * hang. The check therefore has to happen before we touch anything, which is
 * what this file provides -- every constructor calls it first and returns
 * early, leaving the process completely untouched.
 */

#define _GNU_SOURCE
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "nvsnap_intercept.h"

/* Everything CRIU runs during dump/restore lives here: criu itself, the
 * cuda-checkpoint it shells out to, iptables-restore for the network lock,
 * and our own restore helpers. Matching on the directory covers them all
 * without an executable-name list that drifts as the bundle changes. */
#define NVSNAP_BUNDLE_PREFIX "/criu-bundle/"

int nvsnap_self_disabled(void)
{
    /* Resolved once: the answer cannot change within a process image, and the
     * constructors that call this run before threads exist. */
    static int cached = -1;

    if (cached >= 0)
        return cached;

    /* Explicit override, for callers that stage the bundle somewhere else. */
    const char *env = getenv("NVSNAP_INTERCEPT_DISABLE");
    if (env && strcmp(env, "1") == 0) {
        cached = 1;
        return cached;
    }

    char exe[PATH_MAX];
    ssize_t n = readlink("/proc/self/exe", exe, sizeof(exe) - 1);
    if (n > 0) {
        exe[n] = '\0';
        if (strncmp(exe, NVSNAP_BUNDLE_PREFIX,
                    sizeof(NVSNAP_BUNDLE_PREFIX) - 1) == 0) {
            cached = 1;
            return cached;
        }
    }

    cached = 0;
    return cached;
}

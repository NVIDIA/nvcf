/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * abort() Interception for NVSNAP
 *
 * Interposes abort() to:
 * 1. Print a C backtrace before crashing (diagnostic)
 * 2. After CRIU restore, suppress the first abort for a grace period.
 *    vLLM's monitor_engine_cores calls abort() when the engine core
 *    doesn't respond within ~1s. After restore, the engine core is
 *    alive but needs time to resume its heartbeat. Suppressing the
 *    abort gives it that time.
 */

#define _GNU_SOURCE
#include <unistd.h>
#include <dlfcn.h>
#include <execinfo.h>
#include <sys/types.h>
#include <time.h>
#include <stdlib.h>

static void __attribute__((noreturn)) (*real_abort)(void) = NULL;

/* From quiesce.c */
extern int nvsnap_is_restored(void);

/* Signal-safe unsigned-to-string */
static int uint_to_str(unsigned val, char *buf)
{
    char tmp[16];
    int len = 0;
    do { tmp[len++] = '0' + (val % 10); val /= 10; } while (val);
    for (int i = 0; i < len; i++)
        buf[i] = tmp[len - 1 - i];
    return len;
}

void __attribute__((noreturn)) abort(void)
{
    if (!real_abort) {
        real_abort = dlsym(RTLD_NEXT, "abort");
        if (!real_abort)
            _exit(134);
    }

    static const char banner[] = "\n=== NVSNAP ABORT INTERCEPTED ===\n";
    (void)write(STDERR_FILENO, banner, sizeof(banner) - 1);

    /* PID (signal-safe) */
    {
        char buf[32] = "PID: ";
        int pos = 5;
        pos += uint_to_str((unsigned)getpid(), buf + pos);
        buf[pos++] = '\n';
        (void)write(STDERR_FILENO, buf, pos);
    }

    /* C backtrace */
    {
        static const char hdr[] = "Backtrace:\n";
        (void)write(STDERR_FILENO, hdr, sizeof(hdr) - 1);
        void *frames[64];
        int n = backtrace(frames, 64);
        backtrace_symbols_fd(frames, n, STDERR_FILENO);
    }

    static const char end[] = "=== END NVSNAP ABORT ===\n\n";
    (void)write(STDERR_FILENO, end, sizeof(end) - 1);

    /*
     * Post-restore grace period: suppress the abort and return (longjmp
     * style is unsafe here). Instead, just sleep and _exit with a
     * distinct code so we can distinguish "suppressed abort" from
     * "real abort". The sleep gives the engine core time to resume.
     *
     * NOTE: abort() is __noreturn. Suppressing it is undefined behavior
     * per the C standard. But in practice, vLLM's monitor_engine_cores
     * calls abort() from a daemon thread — if we _exit() instead, the
     * process dies cleanly without the SIGABRT cascade that kills all
     * child processes.
     *
     * TODO: A better approach would be to intercept the monitor's
     * poll() timeout or inject a heartbeat. This is a stopgap.
     */
    if (nvsnap_is_restored()) {
        static const char msg[] = "[NVSNAP] Post-restore abort suppressed — sleeping 5s for engine core resume\n";
        (void)write(STDERR_FILENO, msg, sizeof(msg) - 1);
        struct timespec ts = { .tv_sec = 5, .tv_nsec = 0 };
        nanosleep(&ts, NULL);
        static const char msg2[] = "[NVSNAP] Grace period elapsed, proceeding with abort\n";
        (void)write(STDERR_FILENO, msg2, sizeof(msg2) - 1);
    }

    real_abort();
    _exit(134); /* unreachable */
}

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Library initialization and cleanup.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include "nvsnap/gpu/config.h"
#include "nvsnap/gpu/metrics.h"
#include "nvsnap/gpu/interpose.h"
#include "nvsnap/gpu/tracker.h"

/* Global log level — referenced by NVSNAP_GPU_LOG macro. */
int nvsnap_gpu_log_level = 0;

/* Thread-safe logging using write() — no FILE* buffering, no locks.
 * Atomic for messages < 4096 bytes (PIPE_BUF on Linux). */
#include <stdarg.h>
void nvsnap_log_write(int level, const char *fmt, ...)
{
    char buf[4096];
    int off = snprintf(buf, sizeof(buf), "[nvsnap:%d] ", level);
    if (off < 0 || off >= (int)sizeof(buf)) return;

    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(buf + off, sizeof(buf) - (size_t)off, fmt, ap);
    va_end(ap);
    if (n < 0) return;
    off += n;
    if (off >= (int)sizeof(buf) - 1) off = (int)sizeof(buf) - 2;
    buf[off++] = '\n';

    /* write() is async-signal-safe and atomic for < PIPE_BUF bytes. */
    ssize_t __attribute__((unused)) wr = write(STDERR_FILENO, buf, (size_t)off);
}

/*
 * Resolve a real function pointer when RTLD_NEXT fails.
 *
 * This happens when the library containing the symbol (e.g., libnccl.so)
 * was loaded via dlopen AFTER our LD_PRELOAD library. RTLD_NEXT only
 * searches libraries loaded after us in the link chain, but dlopen'd
 * libraries are not in that chain.
 *
 * Strategy: try to dlopen the likely library with RTLD_NOLOAD (returns
 * handle if already loaded, NULL if not) then dlsym from that handle.
 */
static const char *g_lib_search[] = {
    "libnccl.so.2",
    "libnccl.so",
    "libcudart.so",
    "libcudart.so.12",
    "libcuda.so.1",
    "libcuda.so",
    NULL
};

/*
 * Cached handles to real GPU libraries (populated on first resolve).
 * We dlopen with RTLD_NOLOAD first (already loaded by PyTorch) to get
 * a handle, then use dlvsym or iterate to find the real symbol.
 */
static void *g_real_lib_handles[16] = {0};
static int g_handles_count = 0;

static void nvsnap_populate_lib_handles(void)
{
    if (g_handles_count > 0) return;

    for (const char **lib = g_lib_search; *lib; lib++) {
        /* RTLD_NOLOAD: only returns handle if already loaded. */
        void *h = dlopen(*lib, RTLD_LAZY | RTLD_NOLOAD);
        if (h && g_handles_count < 15) {
            g_real_lib_handles[g_handles_count++] = h;
            NVSNAP_GPU_LOG_DEBUG("resolve: found loaded library %s", *lib);
        }
    }

    /* Also try loading them explicitly if not already loaded. */
    for (const char **lib = g_lib_search; *lib; lib++) {
        void *h = dlopen(*lib, RTLD_LAZY | RTLD_GLOBAL);
        if (h && g_handles_count < 15) {
            /* Check if we already have this handle. */
            int dup = 0;
            for (int i = 0; i < g_handles_count; i++) {
                if (g_real_lib_handles[i] == h) { dup = 1; break; }
            }
            if (!dup) {
                g_real_lib_handles[g_handles_count++] = h;
            }
        }
    }
}

void *nvsnap_resolve_real(const char *func_name, const char *lib_hint)
{
    void *sym = NULL;

    /* If caller provides a specific library, try that first. */
    if (lib_hint) {
        void *h = dlopen(lib_hint, RTLD_LAZY | RTLD_NOLOAD);
        if (!h) h = dlopen(lib_hint, RTLD_LAZY);
        if (h) {
            sym = dlsym(h, func_name);
            /* Check it's not our own wrapper (LD_PRELOAD can cause this). */
            Dl_info info;
            if (sym && dladdr(sym, &info) && info.dli_fname &&
                !strstr(info.dli_fname, "libnvsnap_intercept")) {
                return sym;
            }
        }
    }

    /* Search cached library handles. */
    nvsnap_populate_lib_handles();
    for (int i = 0; i < g_handles_count; i++) {
        sym = dlsym(g_real_lib_handles[i], func_name);
        if (sym) {
            /* Verify it's not our own wrapper. */
            Dl_info info;
            if (dladdr(sym, &info) && info.dli_fname &&
                !strstr(info.dli_fname, "libnvsnap_intercept")) {
                return sym;
            }
        }
    }

    /* Try RTLD_NEXT one more time (library may have been loaded since). */
    sym = dlsym(RTLD_NEXT, func_name);
    if (sym) {
        Dl_info info;
        if (dladdr(sym, &info) && info.dli_fname &&
            !strstr(info.dli_fname, "libnvsnap_intercept")) {
            return sym;
        }
    }

    return NULL;
}

/* Thread-local current device. */
_Thread_local int nvsnap_current_device = 0;
/* Thread-local flag: kept for API compatibility but no longer
 * set by stream capture wrappers (which were removed). */
_Thread_local int nvsnap_in_graph_capture = 0;

/*
 * Fork safety: reset NvSnap state in child process after fork().
 *
 * vLLM and other multi-process GPU frameworks use fork() extensively.
 * After fork(), the child inherits parent's mutexes (potentially locked),
 * shared memory (write positions corrupted), and semaphores (undefined).
 * We must reinitialize everything in the child.
 */
static void nvsnap_atfork_child(void)
{
    /* 1. Reset tracker — reconstructs mutexes, clears stale parent state. */
    nvsnap_tracker_reset_after_fork();

    /* 2. Reinit metrics — child gets its own shared memory segment. */
    nvsnap_metrics_destroy();
    nvsnap_metrics_init();

    NVSNAP_GPU_LOG_INFO("NvSnap reinitialized after fork (child pid=%d)",
                      (int)getpid());
}

/* Defined in src/self_disable.c. Declared here rather than pulling in
 * nvsnap_intercept.h, which this GPU sub-module otherwise does not use. */
int nvsnap_self_disabled(void);

__attribute__((constructor(102)))  /* After NvSnap init (101), before NvSnap atfork (103) */
static void nvsnap_gpu_init(void)
{
    if (nvsnap_self_disabled())
        return;

    nvsnap_config_init();

    const NvSnapConfig *cfg = nvsnap_config_get();
    nvsnap_gpu_log_level = cfg->log_level;

    if (cfg->metrics_enabled) {
        nvsnap_metrics_init();
    }

    /* Register fork handler — critical for vLLM and other multi-process frameworks. */
    pthread_atfork(NULL, NULL, nvsnap_atfork_child);

    /*
     * Post-CRIU restore detection.
     *
     * If NVSNAP_RESTORE_DIR is set, this process was checkpointed and is
     * now being restored by CRIU. We need to restore GPU memory from the
     * checkpoint before the application resumes.
     *
     * The restore-entrypoint sets this env var before CRIU restore. After
     * CRIU unfreezes the process, libnvsnap_intercept.so's constructor re-runs
     * (CRIU re-executes constructors on restore for LD_PRELOAD libraries).
     *
     * Actually — CRIU does NOT re-run constructors. The process resumes
     * from where it was frozen. So we need a different detection method.
     *
     * Instead, NvSnap's libnvsnap_intercept.so will call nvsnap_checkpoint_restore()
     * directly when it detects the /run/criu-restored marker. NvSnap just
     * needs to expose the function — which it already does.
     *
     * For standalone (non-NvSnap) restore, use the nvsnap-gpu-restore helper
     * or call the C API from the restore orchestrator.
     */

    NVSNAP_GPU_LOG_INFO("NvSnap loaded (pid=%d)", (int)getpid());
}

__attribute__((destructor))
static void nvsnap_fini(void)
{
    NVSNAP_GPU_LOG_INFO("NvSnap unloading (pid=%d)", (int)getpid());
    nvsnap_metrics_destroy();
}

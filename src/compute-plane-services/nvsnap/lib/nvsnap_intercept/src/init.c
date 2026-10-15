/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NVSNAP Interception Library - Initialization
 * 
 * This library intercepts io_uring and libuv for checkpoint/restore support.
 * CUDA/GPU state is handled externally by cuda-checkpoint (NVIDIA's tool).
 * 
 * Initialization happens automatically via __attribute__((constructor)).
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <stdarg.h>
#include <stdlib.h>
#include <string.h>
#include <dlfcn.h>
#include <pthread.h>
#include <unistd.h>
#include <time.h>

#include <signal.h>

#include "nvsnap_intercept.h"
#include <unistd.h>

/* From io_uring_intercept.c */
void nvsnap_install_uvloop_hook_async(void);
/* From zmq_intercept.c */
void nvsnap_zmq_reinit_all_if_restored(void);
/* From quiesce.c */
void nvsnap_quiesce_init(void);
void nvsnap_start_quiesce_worker(void);
void nvsnap_perform_restore_reinit(void);

static pthread_once_t g_restore_watch_once = PTHREAD_ONCE_INIT;

static void *nvsnap_restore_watch_thread(void *arg) {
    (void)arg;
    for (;;) {
        /*
         * Marker locations (nvsnap#186):
         *   /var/run/nvsnap/.restored   — canonical, always writable.
         *                               Matches NVSNAP_RESTORE_MARKER in
         *                               io_uring_intercept.c +
         *                               libuv_intercept.c. This is the
         *                               one that works when /nvsnap and
         *                               /nvsnap-lib are hostPath read-only
         *                               (the v0.0.20+ production webhook
         *                               injection path — see
         *                               internal/webhook/restore_entrypoint.go).
         *   /nvsnap-lib/.restored,
         *   /nvsnap/.restored           — legacy, written by restore-entrypoint
         *                               when those mounts are writable
         *                               (emptyDir test-workload pattern).
         *                               Keep checking them so existing
         *                               test workloads aren't regressed.
         *   /run/criu-restored        — historical alternate.
         */
        if (access("/var/run/nvsnap/.restored", F_OK) == 0 ||
            access("/nvsnap-lib/.restored", F_OK) == 0 || access("/nvsnap/.restored", F_OK) == 0 ||
            access("/run/criu-restored", F_OK) == 0) {
            NVSNAP_WARN("Restore watch detected marker, triggering full reinit");
            nvsnap_zmq_reinit_all_if_restored();
            nvsnap_perform_restore_reinit();
            break;
        }
        usleep(200 * 1000);
    }
    return NULL;
}

static void nvsnap_start_restore_watch(void) {
    pthread_t tid;
    if (pthread_create(&tid, NULL, nvsnap_restore_watch_thread, NULL) == 0) {
        pthread_detach(tid);
    } else {
        NVSNAP_WARN("Restore watch thread failed to start");
    }
}

/*
 * =============================================================================
 * GLOBAL STATE
 * =============================================================================
 */

static nvsnap_state_t g_state = {
    .initialized = false,
    .enabled = true,
    .init_mutex = PTHREAD_MUTEX_INITIALIZER,
    .log_mutex = PTHREAD_MUTEX_INITIALIZER,
    .log_level = NVSNAP_LOG_INFO,
    .log_file = NULL,
};

nvsnap_state_t* nvsnap_get_state(void) {
    return &g_state;
}

/*
 * =============================================================================
 * LOGGING
 * =============================================================================
 */

static const char* log_level_str[] = {
    "OFF", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"
};

void nvsnap_log(nvsnap_log_level_t level, const char* func, const char* fmt, ...) {
    nvsnap_state_t* state = &g_state;
    
    if (level > state->log_level) {
        return;
    }
    
    pthread_mutex_lock(&state->log_mutex);
    
    FILE* out = state->log_file ? state->log_file : stderr;
    
    /* Timestamp */
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    struct tm tm;
    localtime_r(&ts.tv_sec, &tm);
    
    fprintf(out, "[%02d:%02d:%02d.%03ld] [%s] [%s] ",
            tm.tm_hour, tm.tm_min, tm.tm_sec, ts.tv_nsec / 1000000,
            log_level_str[level], func);
    
    va_list args;
    va_start(args, fmt);
    vfprintf(out, fmt, args);
    va_end(args);
    
    fprintf(out, "\n");
    fflush(out);
    
    pthread_mutex_unlock(&state->log_mutex);
}

/*
 * =============================================================================
 * INITIALIZATION
 * =============================================================================
 */

static void init_logging(nvsnap_state_t* state) {
    /* Log level */
    const char* level_str = getenv(NVSNAP_ENV_LOG_LEVEL);
    if (level_str) {
        state->log_level = atoi(level_str);
        if (state->log_level > NVSNAP_LOG_TRACE) {
            state->log_level = NVSNAP_LOG_TRACE;
        }
    } else {
        state->log_level = NVSNAP_LOG_INFO;  /* Default */
    }
    
    /* Log file */
    const char* log_file = getenv(NVSNAP_ENV_LOG_FILE);
    if (log_file && strcmp(log_file, "stderr") != 0) {
        state->log_file = fopen(log_file, "a");
        if (!state->log_file) {
            fprintf(stderr, "NVSNAP: Failed to open log file %s, using stderr\n", log_file);
            state->log_file = NULL;
        }
    }
}

/* Check if interception is enabled */
static bool check_enabled(void) {
    const char* enabled = getenv(NVSNAP_ENV_ENABLED);
    if (enabled && strcmp(enabled, "0") == 0) {
        return false;
    }
    /* Skip init for processes that must not be intercepted.
     * cuda-checkpoint is called by the CRIU CUDA plugin during restore.
     * The plugin parses cuda-checkpoint's stdout/stderr to extract the
     * restore thread ID. If the intercept library initializes, its log
     * messages corrupt the output, causing GPU resume to fail (tid=0). */
    const char* comm = NULL;
    char buf[256];
    FILE* f = fopen("/proc/self/comm", "r");
    if (f) {
        if (fgets(buf, sizeof(buf), f)) {
            /* Strip trailing newline */
            char* nl = strchr(buf, '\n');
            if (nl) *nl = '\0';
            comm = buf;
        }
        fclose(f);
    }
    if (comm && (strstr(comm, "cuda-checkpoint") != NULL ||
                 strstr(comm, "cuda_checkpoint") != NULL)) {
        return false;
    }
    return true;
}

/* Check if seccomp interception is enabled */
static bool check_seccomp_enabled(void) {
    const char* enabled = getenv(NVSNAP_ENV_SECCOMP_ENABLED);
    if (enabled && strcmp(enabled, "1") == 0) {
        return true;
    }
    return false;
}

/* Check if we're in post-restore mode */
static bool check_post_restore(void) {
    const char* post_restore = getenv(NVSNAP_ENV_POST_RESTORE);
    if (post_restore && strcmp(post_restore, "1") == 0) {
        return true;
    }
    return false;
}

/* No-op signal handler for SIGUSR2 — causes EINTR in blocked syscalls */
static void nvsnap_sigusr2_noop(int sig) { (void)sig; }

int nvsnap_init_explicit(void) {
    nvsnap_state_t* state = &g_state;
    
    pthread_mutex_lock(&state->init_mutex);
    
    if (state->initialized) {
        pthread_mutex_unlock(&state->init_mutex);
        return 0;
    }
    
    /* Check if enabled - allow complete disable for debugging */
    state->enabled = check_enabled();
    if (!state->enabled) {
        /* Silent disable - don't even print anything */
        state->initialized = true;
        pthread_mutex_unlock(&state->init_mutex);
        return 0;
    }
    
    /* Initialize logging first */
    init_logging(state);
    
    NVSNAP_INFO("=== NVSNAP Interception Library Initializing ===");
    NVSNAP_INFO("PID: %d, PPID: %d", getpid(), getppid());
    NVSNAP_INFO("Purpose: io_uring draining + libuv reinit for CRIU checkpoint/restore");
    NVSNAP_INFO("Note: GPU state is handled by cuda-checkpoint (NVIDIA)");

    /* Install no-op SIGUSR2 handler for CRIU restore wakeup.
     * After CRIU restore, library I/O threads are stuck in epoll_wait().
     * The restore-entrypoint sends SIGUSR2 to all threads, causing EINTR,
     * which lets the library event loops continue and detect the restore.
     * SA_RESTART is NOT set — we WANT EINTR to interrupt blocking syscalls. */
    {
        struct sigaction sa;
        memset(&sa, 0, sizeof(sa));
        sa.sa_handler = nvsnap_sigusr2_noop;
        sa.sa_flags = 0;
        sigemptyset(&sa.sa_mask);
        if (sigaction(SIGUSR2, &sa, NULL) == 0) {
            NVSNAP_INFO("Installed SIGUSR2 handler for CRIU restore wakeup");
        }
    }

    /* Install uvloop hook when Python initializes */
    nvsnap_install_uvloop_hook_async();

    /* Ensure we trigger restore reinit even if no intercepts fire */
    pthread_once(&g_restore_watch_once, nvsnap_start_restore_watch);

    /* If we were restored, kick off reinit (uvloop handles its own fork via native patch).
     * /var/run/nvsnap/.restored is the canonical marker that always works — the legacy
     * /nvsnap and /nvsnap-lib paths are checked too so existing test workloads (which use
     * writable emptyDirs there) stay compatible. See nvsnap_restore_watch_thread above
     * for the full rationale (nvsnap#186). */
    if (access("/var/run/nvsnap/.restored", F_OK) == 0 ||
        access("/nvsnap-lib/.restored", F_OK) == 0 || access("/nvsnap/.restored", F_OK) == 0) {
        nvsnap_zmq_reinit_all_if_restored();
    }
    
    /* Check if seccomp interception is enabled */
    if (check_seccomp_enabled()) {
        NVSNAP_INFO("seccomp interception enabled via %s", NVSNAP_ENV_SECCOMP_ENABLED);
        
        if (nvsnap_seccomp_install_filter() == 0) {
            NVSNAP_INFO("seccomp filter installed successfully");
            
            /* Check if post-restore mode */
            if (check_post_restore()) {
                NVSNAP_INFO("Post-restore mode detected - will monitor io_uring for healing");
                nvsnap_seccomp_set_post_restore(true);
            }
        } else {
            NVSNAP_ERROR("Failed to install seccomp filter");
        }
    } else {
        NVSNAP_DEBUG("seccomp interception disabled (set %s=1 to enable)", 
                    NVSNAP_ENV_SECCOMP_ENABLED);
    }
    
    /* Initialize quiesce module: installs SIGUSR1/SIGUSR2 handlers (if
     * NVSNAP_QUIESCE_SIGNALS=1), sets up ACK pipe, starts worker thread.
     * Without this, SIGUSR2 resume never fires and quiesce spin-loops forever. */
    nvsnap_quiesce_init();

    /* Ensure quiesce worker thread runs even if NVSNAP_QUIESCE_SIGNALS is not set.
     * File-based triggers work without signals. */
    nvsnap_start_quiesce_worker();

    state->initialized = true;

    NVSNAP_INFO("=== NVSNAP Initialization Complete ===");
    
    pthread_mutex_unlock(&state->init_mutex);
    return 0;
}

/* Automatic initialization via constructor */
__attribute__((constructor(101)))  /* Priority 101 = run early but after libc */
void nvsnap_init(void) {
    if (nvsnap_self_disabled())
        return;
    nvsnap_init_explicit();
}

/* Cleanup on unload */
__attribute__((destructor))
void nvsnap_fini(void) {
    nvsnap_state_t* state = &g_state;
    
    if (!state->initialized) {
        return;
    }
    
    NVSNAP_INFO("=== NVSNAP Shutting Down ===");
    
    /* Print quiesce state if any io_uring or libuv was tracked */
    nvsnap_dump_quiesce_state(stderr);
    
    if (state->log_file && state->log_file != stderr) {
        fclose(state->log_file);
    }
}

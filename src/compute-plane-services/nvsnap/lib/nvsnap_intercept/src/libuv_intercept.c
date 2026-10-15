/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * libuv Interception for NVSNAP
 *
 * This module intercepts libuv calls to:
 * 1. Track all uv_loop_t instances AND all uv_handle_t instances
 * 2. Call uv_loop_fork() after CRIU restore
 * 3. Reinitialize individual handles that become stale after restore
 *
 * The problem with uvloop after CRIU restore:
 * - libuv's internal state (epoll fd, signal handlers) is stale
 * - uvloop's Python objects contain cached C pointers to uv_handle_t
 * - uv_loop_fork() fixes loop backend BUT NOT individual handles
 *
 * Our solution:
 * - Track ALL handles (not just loops) as they're created
 * - On restore, mark all handles as "stale" 
 * - Before any handle operation, check if handle needs reinit
 * - Reinit handles lazily on first post-restore use
 *
 * Key insight: libuv handles have a consistent structure where the first
 * field is always a pointer to the loop. We can use this to validate handles.
 *
 * Build:
 *   Part of libnvsnap_intercept.so
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <dlfcn.h>
#include <pthread.h>
#include <stdint.h>
#include <link.h>
#include <unistd.h>

#include "nvsnap_intercept.h"

/* External functions from quiesce.c */
extern int nvsnap_track_libuv_loop(void* loop);
extern int nvsnap_ensure_libuv_loop_ready(void* loop);
extern int nvsnap_perform_quiescence(void);

/*
 * =============================================================================
 * HANDLE TRACKING
 * =============================================================================
 * 
 * We track ALL libuv handles to enable post-restore reinitialization.
 * Each handle type needs different reinit logic.
 */

typedef enum {
    NVSNAP_UV_UNKNOWN = 0,
    NVSNAP_UV_ASYNC,
    NVSNAP_UV_CHECK,
    NVSNAP_UV_FS_EVENT,
    NVSNAP_UV_FS_POLL,
    NVSNAP_UV_IDLE,
    NVSNAP_UV_PIPE,
    NVSNAP_UV_POLL,
    NVSNAP_UV_PREPARE,
    NVSNAP_UV_PROCESS,
    NVSNAP_UV_SIGNAL,
    NVSNAP_UV_TCP,
    NVSNAP_UV_TIMER,
    NVSNAP_UV_TTY,
    NVSNAP_UV_UDP,
} nvsnap_uv_handle_type_t;

typedef struct nvsnap_uv_handle {
    void* handle;                   /* uv_handle_t* pointer */
    void* loop;                     /* Owning loop */
    nvsnap_uv_handle_type_t type;    /* Handle type for reinit dispatch */
    uint64_t generation;            /* Generation when created */
    int needs_reinit;               /* Flag: needs reinit after restore */
    int has_signum;                /* Signal signum known */
    int signum;                     /* Signal number (if has_signum) */
    struct nvsnap_uv_handle* next;   /* Linked list */
} nvsnap_uv_handle_t;

#define MAX_TRACKED_HANDLES 4096
static nvsnap_uv_handle_t* g_handles = NULL;
static int g_handle_count = 0;
static pthread_mutex_t g_handle_mutex = PTHREAD_MUTEX_INITIALIZER;
static uint64_t g_generation = 1;  /* Increments on each restore */

/* Forward declaration - full definition with restore detection below */
static int g_libuv_restored = 0;
static void nvsnap_dump_handles(FILE* out);

/* Minimal public prefix of uv_handle_t (data, loop) */
typedef struct {
    void* data;
    void* loop;
} nvsnap_uv_handle_public_t;

static void* get_handle_loop(void* handle) {
    if (!handle) return NULL;
    nvsnap_uv_handle_public_t* pub = (nvsnap_uv_handle_public_t*)handle;
    return pub->loop;
}

/* Track a handle */
static void track_handle(void* handle, void* loop, nvsnap_uv_handle_type_t type) {
    if (!handle) return;
    
    pthread_mutex_lock(&g_handle_mutex);
    
    /* Check if already tracked */
    for (nvsnap_uv_handle_t* h = g_handles; h; h = h->next) {
        if (h->handle == handle) {
            pthread_mutex_unlock(&g_handle_mutex);
            return;
        }
    }
    
    /* Add new entry */
    nvsnap_uv_handle_t* entry = malloc(sizeof(nvsnap_uv_handle_t));
    if (!entry) {
        pthread_mutex_unlock(&g_handle_mutex);
        return;
    }
    
    entry->handle = handle;
    entry->loop = loop;
    entry->type = type;
    entry->generation = g_generation;
    entry->needs_reinit = 0;
    entry->has_signum = 0;
    entry->signum = 0;
    entry->next = g_handles;
    g_handles = entry;
    g_handle_count++;
    
    NVSNAP_DEBUG("Tracked handle %p type=%d loop=%p (total=%d)", 
                handle, type, loop, g_handle_count);
    
    pthread_mutex_unlock(&g_handle_mutex);
}

/* Untrack a handle (on close) */
static void untrack_handle(void* handle) {
    if (!handle) return;
    
    pthread_mutex_lock(&g_handle_mutex);
    
    nvsnap_uv_handle_t** pp = &g_handles;
    while (*pp) {
        if ((*pp)->handle == handle) {
            nvsnap_uv_handle_t* to_free = *pp;
            *pp = (*pp)->next;
            free(to_free);
            g_handle_count--;
            NVSNAP_DEBUG("Untracked handle %p (total=%d)", handle, g_handle_count);
            pthread_mutex_unlock(&g_handle_mutex);
            return;
        }
        pp = &(*pp)->next;
    }
    
    pthread_mutex_unlock(&g_handle_mutex);
}

/* Find a tracked handle */
static nvsnap_uv_handle_t* find_handle(void* handle) {
    for (nvsnap_uv_handle_t* h = g_handles; h; h = h->next) {
        if (h->handle == handle) {
            return h;
        }
    }
    return NULL;
}

static void set_handle_signum(void* handle, int signum) {
    if (!handle) return;
    pthread_mutex_lock(&g_handle_mutex);
    nvsnap_uv_handle_t* h = find_handle(handle);
    if (h) {
        h->has_signum = 1;
        h->signum = signum;
        NVSNAP_DEBUG("Signal handle %p updated signum=%d", handle, signum);
    } else {
        NVSNAP_DEBUG("Signal handle %p not tracked; cannot record signum=%d",
                    handle, signum);
    }
    pthread_mutex_unlock(&g_handle_mutex);
}

/* Mark all handles as needing reinit (called on restore detection) */
void nvsnap_mark_handles_for_reinit(void) {
    pthread_mutex_lock(&g_handle_mutex);
    
    g_generation++;
    int count = 0;
    
    for (nvsnap_uv_handle_t* h = g_handles; h; h = h->next) {
        h->needs_reinit = 1;
        count++;
    }
    
    NVSNAP_INFO("Marked %d handles for reinit (generation=%lu)", count, g_generation);
    
    pthread_mutex_unlock(&g_handle_mutex);
}

/*
 * =============================================================================
 * HANDLE REINITIALIZATION
 * =============================================================================
 *
 * Different handle types need different reinit strategies.
 * The key insight is that most handles just need their internal
 * state reset - the Python/application-level state is still valid.
 */

/* (Function pointers are now obtained dynamically via get_real_libuv_func) */

/*
 * Reinitialize a handle after restore.
 * 
 * Strategy varies by type:
 * - Timer: Just needs loop reinit, timer state preserved
 * - Async: Needs loop reinit, internal eventfd/pipe recreated by uv_loop_fork
 * - Process: Most complex - child process relationship may need verification
 * - Signal: uv_loop_fork should handle this
 * - TCP/UDP/Pipe: Socket state needs validation
 */
static int reinit_handle(nvsnap_uv_handle_t* h) {
    if (!h || !h->needs_reinit) return 0;
    
    NVSNAP_DEBUG("Reinitializing handle %p type=%d", h->handle, h->type);
    
    /* First, ensure the loop is reinitialized */
    nvsnap_ensure_libuv_loop_ready(h->loop);
    
    /*
     * For most handle types, uv_loop_fork() has already done the heavy lifting.
     * The handles should work as long as:
     * 1. The loop is valid
     * 2. The handle's loop pointer matches
     * 
     * We validate this by checking if the handle's loop pointer (first field
     * in all uv_handle_t structures) matches what we expect.
     */
    
    /* uv_handle_t public prefix: data, loop */
    void* handle_loop = get_handle_loop(h->handle);
    
    if (handle_loop != h->loop) {
        NVSNAP_WARN("Handle %p loop pointer mismatch: expected %p, got %p",
                   h->handle, h->loop, handle_loop);
        /* This is bad - handle is corrupted or loop was replaced */
        /* For now, we can't fix this without more invasive changes */
        return -1;
    }
    
    switch (h->type) {
        case NVSNAP_UV_PROCESS:
            /*
             * Process handles are tricky. After restore:
             * - The child PID field should still be valid
             * - The child process was also restored (same process tree)
             * - The exit callback and status fields should be intact
             * 
             * Main concern: signal handling for SIGCHLD
             * uv_loop_fork() should reinit the signal infrastructure.
             * 
             * We mark it as reinited and hope for the best.
             * If it fails, we'll catch it in the error handling.
             */
            NVSNAP_INFO("Process handle %p: marked as reinited (child should be valid)", 
                       h->handle);
            break;
            
        case NVSNAP_UV_SIGNAL:
            /*
             * Signal handles need the loop's signal infrastructure.
             * uv_loop_fork() should have reinited this.
             */
            NVSNAP_DEBUG("Signal handle %p: relying on uv_loop_fork reinit", h->handle);
            break;
            
        case NVSNAP_UV_TIMER:
        case NVSNAP_UV_IDLE:
        case NVSNAP_UV_PREPARE:
        case NVSNAP_UV_CHECK:
            /*
             * These are simple handles that just need the loop to work.
             * uv_loop_fork() is sufficient.
             */
            NVSNAP_DEBUG("Simple handle %p type=%d: loop reinit sufficient", 
                        h->handle, h->type);
            break;
            
        case NVSNAP_UV_ASYNC:
            /*
             * Async handles use an eventfd or pipe pair.
             * uv_loop_fork() recreates these.
             * The handle should work after loop reinit.
             */
            NVSNAP_DEBUG("Async handle %p: relying on uv_loop_fork", h->handle);
            break;
            
        case NVSNAP_UV_TCP:
        case NVSNAP_UV_UDP:
        case NVSNAP_UV_PIPE:
            /*
             * Socket handles are complex:
             * - The FD was restored by CRIU
             * - But the socket state might need validation
             * 
             * For now, we trust CRIU's socket restoration.
             * If there are issues, we'll add specific handling.
             */
            NVSNAP_DEBUG("Socket handle %p type=%d: trusting CRIU socket restore",
                        h->handle, h->type);
            break;
            
        case NVSNAP_UV_POLL:
            /*
             * Poll handles wrap an external FD.
             * The FD should be valid after CRIU restore.
             * The poll registration in the loop is handled by uv_loop_fork.
             */
            NVSNAP_DEBUG("Poll handle %p: relying on uv_loop_fork", h->handle);
            break;
            
        default:
            NVSNAP_DEBUG("Unknown handle type %d: assuming loop reinit sufficient", h->type);
            break;
    }
    
    h->needs_reinit = 0;
    h->generation = g_generation;
    
    return 0;
}

/*
 * Ensure a handle is ready for use after restore.
 * Called before any operation on the handle.
 */
static void ensure_handle_ready(void* handle) {
    if (!g_libuv_restored || !handle) return;
    
    pthread_mutex_lock(&g_handle_mutex);
    
    nvsnap_uv_handle_t* h = find_handle(handle);
    if (h && h->needs_reinit) {
        reinit_handle(h);
    }
    
    pthread_mutex_unlock(&g_handle_mutex);
}

/* 
 * Function pointers are obtained dynamically via get_real_libuv_func()
 * to avoid issues with dlvsym and versioned symbols.
 * Each intercepted function caches its real function pointer locally.
 */

/* 
 * Macro for intercepting loop functions.
 * Uses get_real_libuv_func() which properly bypasses our dlsym hook.
 */
#define INTERCEPT_LOOP_FN(name, ...) \
    static int (*real_fn)() = NULL; \
    if (!real_fn) { \
        real_fn = get_real_libuv_func(#name); \
        if (!real_fn) { \
            NVSNAP_WARN(#name " not found"); \
            return -1; \
        } \
    } \
    if (check_if_restored()) { \
        nvsnap_ensure_libuv_loop_ready(loop); \
    } \
    return real_fn(__VA_ARGS__)

/* Macro for void loop functions */
#define INTERCEPT_LOOP_VOID_FN(name, ...) \
    static void (*real_fn)() = NULL; \
    if (!real_fn) { \
        real_fn = get_real_libuv_func(#name); \
        if (!real_fn) { \
            NVSNAP_WARN(#name " not found"); \
            return; \
        } \
    } \
    if (check_if_restored()) { \
        nvsnap_ensure_libuv_loop_ready(loop); \
    } \
    real_fn(__VA_ARGS__)

/*
 * =============================================================================
 * LOOP LIFECYCLE
 * =============================================================================
 */

/*
 * LIBUV INTERCEPTION STRATEGY
 * 
 * Problem: We need to track libuv loops and reinitialize them after CRIU restore.
 * 
 * Key insight: Our dlsym hook in dlopen_hook.c ONLY intercepts "cu*" and "nccl*"
 * symbols. So dlsym(RTLD_NEXT, "uv_*") will pass through to real dlsym and work!
 * 
 * The previous issue was using dlvsym with empty version string (""), which
 * fails for unversioned symbols like libuv. We now use dlsym directly.
 * 
 * Restore detection: CRIU preserves the original environment, so NVSNAP_RESTORED=1
 * won't be set in the restored process. Instead, we use a file marker:
 *   /var/run/nvsnap/.restored (created by restore-entrypoint before CRIU restore)
 */

/* Real dlsym - obtained at init time to avoid recursion */
static void* (*real_libuv_dlsym)(void*, const char*) = NULL;
static void* (*real_dlopen)(const char*, int) = NULL;
static void* uvloop_handle = NULL;

typedef struct {
    void* handle;
} nvsnap_uvloop_find_ctx_t;

static int nvsnap_find_uvloop_cb(struct dl_phdr_info* info, size_t size, void* data) {
    (void)size;
    if (!info || !info->dlpi_name || !info->dlpi_name[0]) {
        return 0;
    }
    if (strstr(info->dlpi_name, "uvloop") && strstr(info->dlpi_name, ".so")) {
        nvsnap_uvloop_find_ctx_t* out = (nvsnap_uvloop_find_ctx_t*)data;
        if (real_dlopen) {
            out->handle = real_dlopen(info->dlpi_name, RTLD_LAZY | RTLD_NOLOAD);
            if (out->handle) {
                NVSNAP_DEBUG("Opened uvloop module: %s", info->dlpi_name);
                return 1; /* stop iteration */
            }
        }
    }
    return 0;
}

/* Marker file for restore detection (g_libuv_restored defined at top of file) */
#define NVSNAP_RESTORE_MARKER "/var/run/nvsnap/.restored"

/* Handle for libuv library */
static void* libuv_handle = NULL;

/* Get real libuv function - use dlopen to get explicit handle */
static void* get_real_libuv_func(const char* name) {
    if (!real_libuv_dlsym) {
        /* Bootstrap: get real dlsym via dlvsym (we don't hook dlvsym) */
        real_libuv_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.34");
        if (!real_libuv_dlsym) {
            real_libuv_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
        }
        if (!real_libuv_dlsym) {
            real_libuv_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.17");
        }
    }
    if (!real_libuv_dlsym) {
        NVSNAP_WARN("Could not get real dlsym");
        return NULL;
    }
    
    /*
     * Strategy: Try to get an explicit handle to libuv, then use dlsym on that handle.
     * This avoids finding our own intercepted symbols via RTLD_DEFAULT.
     * 
     * uvloop bundles its own libuv, so we check for that first.
     */
    if (!real_dlopen) {
        real_dlopen = dlvsym(RTLD_NEXT, "dlopen", "GLIBC_2.34");
        if (!real_dlopen) {
            real_dlopen = dlvsym(RTLD_NEXT, "dlopen", "GLIBC_2.2.5");
        }
        if (!real_dlopen) {
            real_dlopen = dlvsym(RTLD_NEXT, "dlopen", "GLIBC_2.17");
        }
    }

    if (!libuv_handle) {
        /* Try uvloop's bundled libuv first (it's typically at this path) */
        
        if (real_dlopen) {
            /* Try uvloop's bundled libuv - use RTLD_NOLOAD to check if already loaded */
            libuv_handle = real_dlopen("libuv.so.1", RTLD_LAZY | RTLD_NOLOAD);
            if (!libuv_handle) {
                libuv_handle = real_dlopen("libuv.so", RTLD_LAZY | RTLD_NOLOAD);
            }

            /* If not loaded yet, try loading directly (may be in LD_LIBRARY_PATH) */
            if (!libuv_handle) {
                libuv_handle = real_dlopen("libuv.so.1", RTLD_LAZY);
            }
            if (!libuv_handle) {
                libuv_handle = real_dlopen("libuv.so", RTLD_LAZY);
            }

            if (libuv_handle) {
                NVSNAP_DEBUG("Opened libuv library: %p", libuv_handle);
            }
        }
    }
    
    void* func = NULL;
    
    /* Try using explicit libuv handle first */
    if (libuv_handle) {
        func = real_libuv_dlsym(libuv_handle, name);
        if (func) {
            NVSNAP_DEBUG("Found %s in libuv handle: %p", name, func);
            return func;
        }
    }
    
    /* Fallback: use RTLD_NEXT to skip our library and find the next one */
    func = real_libuv_dlsym(RTLD_NEXT, name);
    if (func) {
        NVSNAP_DEBUG("Found %s via RTLD_NEXT: %p", name, func);
        return func;
    }

    /* Final fallback: resolve from uvloop module if it bundles libuv */
    if (!uvloop_handle && real_dlopen) {
        nvsnap_uvloop_find_ctx_t ctx = {0};
        (void)dl_iterate_phdr(nvsnap_find_uvloop_cb, &ctx);
        if (ctx.handle) {
            uvloop_handle = ctx.handle;
        }
    }

    if (uvloop_handle) {
        func = real_libuv_dlsym(uvloop_handle, name);
        if (func) {
            NVSNAP_DEBUG("Found %s in uvloop module: %p", name, func);
            return func;
        }
    }
    
    NVSNAP_DEBUG("Could not find libuv function: %s", name);
    return NULL;
}

/* Check if we're in a restored process */
static int check_if_restored(void) {
    if (g_libuv_restored) return 1;
    
    /* Check env var (might be set by wrapper scripts) */
    if (getenv("NVSNAP_RESTORED")) {
        g_libuv_restored = 1;
        return 1;
    }
    
    /* Check marker file */
    if (access(NVSNAP_RESTORE_MARKER, F_OK) == 0) {
        g_libuv_restored = 1;
        return 1;
    }
    
    return 0;
}

/* Called by quiesce.c when restore is detected */
void nvsnap_libuv_enable_interception(void) {
    g_libuv_restored = 1;
    NVSNAP_INFO("libuv restore mode activated");
}

/*
 * Note: We use weak symbols so that if libuv is statically linked (like in uvloop),
 * our interception functions won't override the internal symbols.
 * 
 * However, this doesn't always work because:
 * 1. LD_PRELOAD typically overrides regardless of weak/strong
 * 2. Cython extensions may use different symbol resolution
 * 
 * So we also check if we can find the real function, and if not, we need a fallback.
 */

/* Flag to disable libuv interception if real functions not available */
static int g_libuv_interception_enabled = -1;  /* -1 = not checked yet */

static int check_libuv_available(void) {
    if (g_libuv_interception_enabled == -1) {
        void* test = get_real_libuv_func("uv_loop_init");
        g_libuv_interception_enabled = (test != NULL) ? 1 : 0;
        if (!g_libuv_interception_enabled) {
            NVSNAP_INFO("libuv not dynamically available (may be statically linked in uvloop) - "
                       "disabling libuv interception");
        } else {
            NVSNAP_INFO("libuv dynamically available - interception enabled");
        }
    }
    return g_libuv_interception_enabled;
}

int uv_loop_init(void* loop) {
    /* Check if libuv is available for interception */
    if (!check_libuv_available()) {
        /* libuv is statically linked or not available.
         * This function shouldn't even be called in that case,
         * but if it is, we need to fail gracefully. */
        NVSNAP_WARN("uv_loop_init called but libuv not available for interception");
        return -1;  /* EPERM - will cause uvloop to error */
    }
    
    static int (*real_fn)(void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_loop_init");
        if (!real_fn) {
            NVSNAP_WARN("uv_loop_init: could not find real function");
            return -1;
        }
    }
    
    NVSNAP_DEBUG("uv_loop_init(%p)", loop);
    
    int ret = real_fn(loop);
    
    if (ret == 0) {
        /* Track the loop for post-restore reinit */
        nvsnap_track_libuv_loop(loop);
        NVSNAP_DEBUG("Tracked libuv loop %p", loop);
    }
    
    return ret;
}

int uv_loop_close(void* loop) {
    static int (*real_fn)(void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_loop_close");
        if (!real_fn) return -1;
    }
    
    NVSNAP_DEBUG("uv_loop_close(%p)", loop);
    
    /* No need to untrack - loop will be reused or freed */
    return real_fn(loop);
}

/*
 * =============================================================================
 * LOOP OPERATIONS - These need reinit after restore
 * =============================================================================
 */

int uv_run(void* loop, int mode) {
    INTERCEPT_LOOP_FN(uv_run, loop, mode);
}

int uv_loop_alive(void* loop) {
    INTERCEPT_LOOP_FN(uv_loop_alive, loop);
}

int uv_backend_fd(void* loop) {
    INTERCEPT_LOOP_FN(uv_backend_fd, loop);
}

int uv_backend_timeout(void* loop) {
    INTERCEPT_LOOP_FN(uv_backend_timeout, loop);
}

void uv_stop(void* loop) {
    INTERCEPT_LOOP_VOID_FN(uv_stop, loop);
}

void uv_update_time(void* loop) {
    INTERCEPT_LOOP_VOID_FN(uv_update_time, loop);
}

uint64_t uv_now(void* loop) {
    static uint64_t (*real_fn)(void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_now");
        if (!real_fn) return 0;
    }
    if (check_if_restored()) {
        nvsnap_ensure_libuv_loop_ready(loop);
    }
    return real_fn(loop);
}

/*
 * =============================================================================
 * HANDLE INITIALIZATION - Track handles AND bind to loop
 * =============================================================================
 */

/* Macro for init functions that also track the handle */
#define INTERCEPT_INIT_FN(name, type, ...) \
    static int (*real_fn)() = NULL; \
    if (!real_fn) { \
        real_fn = get_real_libuv_func(#name); \
        if (!real_fn) { \
            NVSNAP_WARN(#name " not found"); \
            return -1; \
        } \
    } \
    if (check_if_restored()) { \
        nvsnap_ensure_libuv_loop_ready(loop); \
    } \
    int ret = real_fn(__VA_ARGS__); \
    if (ret == 0) { \
        track_handle(handle, loop, type); \
    } \
    return ret

int uv_tcp_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_tcp_init, NVSNAP_UV_TCP, loop, handle);
}

int uv_tcp_init_ex(void* loop, void* handle, unsigned int flags) {
    INTERCEPT_INIT_FN(uv_tcp_init_ex, NVSNAP_UV_TCP, loop, handle, flags);
}

int uv_udp_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_udp_init, NVSNAP_UV_UDP, loop, handle);
}

int uv_udp_init_ex(void* loop, void* handle, unsigned int flags) {
    INTERCEPT_INIT_FN(uv_udp_init_ex, NVSNAP_UV_UDP, loop, handle, flags);
}

int uv_pipe_init(void* loop, void* handle, int ipc) {
    INTERCEPT_INIT_FN(uv_pipe_init, NVSNAP_UV_PIPE, loop, handle, ipc);
}

int uv_tty_init(void* loop, void* handle, int fd, int readable) {
    INTERCEPT_INIT_FN(uv_tty_init, NVSNAP_UV_TTY, loop, handle, fd, readable);
}

int uv_poll_init(void* loop, void* handle, int fd) {
    INTERCEPT_INIT_FN(uv_poll_init, NVSNAP_UV_POLL, loop, handle, fd);
}

int uv_poll_init_socket(void* loop, void* handle, int socket) {
    INTERCEPT_INIT_FN(uv_poll_init_socket, NVSNAP_UV_POLL, loop, handle, socket);
}

int uv_timer_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_timer_init, NVSNAP_UV_TIMER, loop, handle);
}

int uv_prepare_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_prepare_init, NVSNAP_UV_PREPARE, loop, handle);
}

int uv_check_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_check_init, NVSNAP_UV_CHECK, loop, handle);
}

int uv_idle_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_idle_init, NVSNAP_UV_IDLE, loop, handle);
}

int uv_async_init(void* loop, void* handle, void* cb) {
    INTERCEPT_INIT_FN(uv_async_init, NVSNAP_UV_ASYNC, loop, handle, cb);
}

int uv_signal_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_signal_init, NVSNAP_UV_SIGNAL, loop, handle);
}

int uv_fs_event_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_fs_event_init, NVSNAP_UV_FS_EVENT, loop, handle);
}

int uv_fs_poll_init(void* loop, void* handle) {
    INTERCEPT_INIT_FN(uv_fs_poll_init, NVSNAP_UV_FS_POLL, loop, handle);
}

/*
 * =============================================================================
 * PROCESS HANDLING - Critical for uvloop subprocesses
 * =============================================================================
 *
 * uv_spawn is particularly important because:
 * - vLLM uses multiprocessing with spawn context
 * - uvloop creates subprocess handles (UVProcess)
 * - After restore, these handles need to maintain connection to child
 *
 * Key insight: CRIU restores the entire process tree, so:
 * - Child processes ARE restored with their original PIDs
 * - The uv_process_t handle's pid field is still valid
 * - We just need to ensure signal handling works
 */

int uv_spawn(void* loop, void* handle, void* options) {
    static int (*real_fn)(void*, void*, void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_spawn");
        if (!real_fn) {
            NVSNAP_WARN("uv_spawn not found");
            return -1;
        }
    }
    
    if (check_if_restored()) {
        nvsnap_ensure_libuv_loop_ready(loop);
    }
    
    NVSNAP_DEBUG("uv_spawn(loop=%p, handle=%p, options=%p)", loop, handle, options);
    
    int ret = real_fn(loop, handle, options);
    
    if (ret == 0) {
        /* Track this process handle - critical for post-restore reinit */
        track_handle(handle, loop, NVSNAP_UV_PROCESS);
        NVSNAP_INFO("uv_spawn succeeded: handle=%p (process handle tracked)", handle);
    } else {
        NVSNAP_DEBUG("uv_spawn failed: %d", ret);
    }
    
    return ret;
}

/*
 * =============================================================================
 * HANDLE CLOSE - Untrack handles when they're closed
 * =============================================================================
 */

void uv_close(void* handle, void* close_cb) {
    static void (*real_fn)(void*, void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_close");
        if (!real_fn) {
            NVSNAP_WARN("uv_close not found");
            return;
        }
    }
    
    /* Ensure handle is reinited before close (in case close does cleanup) */
    if (check_if_restored()) {
        ensure_handle_ready(handle);
    }
    
    NVSNAP_DEBUG("uv_close(handle=%p, cb=%p)", handle, close_cb);
    
    /* Untrack the handle */
    untrack_handle(handle);
    
    real_fn(handle, close_cb);
}

/*
 * =============================================================================
 * HANDLE OPERATIONS - Ensure handle is ready before use
 * =============================================================================
 */

/* Intercept handle operations to ensure reinit before use */

/* Macro for handle operations */
#define INTERCEPT_HANDLE_OP(name, ...) \
    static int (*real_fn)() = NULL; \
    if (!real_fn) { \
        real_fn = get_real_libuv_func(#name); \
        if (!real_fn) return -1; \
    } \
    if (check_if_restored()) { \
        ensure_handle_ready(handle); \
    } \
    return real_fn(__VA_ARGS__)

/* Timer operations */
int uv_timer_start(void* handle, void* cb, uint64_t timeout, uint64_t repeat) {
    INTERCEPT_HANDLE_OP(uv_timer_start, handle, cb, timeout, repeat);
}

int uv_timer_stop(void* handle) {
    INTERCEPT_HANDLE_OP(uv_timer_stop, handle);
}

/* Signal operations */
int uv_signal_start(void* handle, void* cb, int signum) {
    NVSNAP_DEBUG("uv_signal_start(handle=%p, signum=%d)", handle, signum);
    set_handle_signum(handle, signum);
    INTERCEPT_HANDLE_OP(uv_signal_start, handle, cb, signum);
}

int uv_signal_start_oneshot(void* handle, void* cb, int signum) {
    set_handle_signum(handle, signum);
    INTERCEPT_HANDLE_OP(uv_signal_start_oneshot, handle, cb, signum);
}

int uv_signal_stop(void* handle) {
    INTERCEPT_HANDLE_OP(uv_signal_stop, handle);
}

/* Async send - important for cross-thread wakeup */
int uv_async_send(void* handle) {
    INTERCEPT_HANDLE_OP(uv_async_send, handle);
}

/* Process kill - used to send signals to child processes */
int uv_process_kill(void* handle, int signum) {
    NVSNAP_DEBUG("uv_process_kill(handle=%p, signum=%d)", handle, signum);
    INTERCEPT_HANDLE_OP(uv_process_kill, handle, signum);
}

/*
 * =============================================================================
 * SPECIAL: uv_loop_fork PASSTHROUGH
 * =============================================================================
 *
 * We don't intercept uv_loop_fork itself - we call it internally.
 * This is a passthrough for apps that call it directly.
 */

int uv_loop_fork_passthrough(void* loop) {
    static int (*real_fn)(void*) = NULL;
    if (!real_fn) {
        real_fn = get_real_libuv_func("uv_loop_fork");
        if (!real_fn) {
            NVSNAP_WARN("uv_loop_fork not found");
            return -1;
        }
    }
    
    NVSNAP_DEBUG("uv_loop_fork(%p) passthrough", loop);
    return real_fn(loop);
}

/*
 * =============================================================================
 * UVLOOP-SPECIFIC HANDLING
 * =============================================================================
 *
 * uvloop uses libuv underneath, so our libuv interception catches it.
 * However, uvloop also has Python-level cached state that we can't fix
 * from C code.
 *
 * For uvloop, the remaining issues after uv_loop_fork() are:
 * 
 * 1. UVProcess._handle - points to stale uv_process_t
 *    - This causes crashes when subprocess.wait() is called
 *    - Solution: Python-level patch in sitecustomize.py
 *
 * 2. Signal handlers - uvloop caches signal handler state
 *    - uv_loop_fork() should fix this
 *
 * 3. Child watchers - for monitoring subprocesses
 *    - Partially fixed by uv_loop_fork()
 *    - May need Python-level reinit
 *
 * The comprehensive solution requires:
 * - This C library for libuv/io_uring (what we're doing)
 * - Python sitecustomize.py for uvloop Python objects
 * - OR: Patching uvloop source (better long-term)
 */

/*
 * Debug helper: Check if we're in a uvloop context
 */
static int is_uvloop_loaded(void) {
    /* Check if uvloop's internal symbols are present */
    void* uvloop_run = dlsym(RTLD_DEFAULT, "uvloop_run");
    return uvloop_run != NULL;
}

/*
 * Called when restore is detected - marks all handles for reinit
 */
void nvsnap_libuv_on_restore(void) {
    g_libuv_restored = 1;
    nvsnap_mark_handles_for_reinit();
    nvsnap_dump_handles(stderr);
}

/*
 * Diagnostic: dump all tracked handles
 */
void nvsnap_dump_handles(FILE* out) {
    pthread_mutex_lock(&g_handle_mutex);
    
    fprintf(out, "\n=== Tracked libuv handles (%d) ===\n", g_handle_count);
    
    const char* type_names[] = {
        "UNKNOWN", "ASYNC", "CHECK", "FS_EVENT", "FS_POLL",
        "IDLE", "PIPE", "POLL", "PREPARE", "PROCESS",
        "SIGNAL", "TCP", "TIMER", "TTY", "UDP"
    };
    
    for (nvsnap_uv_handle_t* h = g_handles; h; h = h->next) {
        const char* type_name = (h->type < sizeof(type_names)/sizeof(type_names[0])) 
                                ? type_names[h->type] : "?";
        if (h->has_signum) {
            fprintf(out, "  handle=%p loop=%p type=%s gen=%lu needs_reinit=%d signum=%d\n",
                    h->handle, h->loop, type_name, h->generation, h->needs_reinit,
                    h->signum);
        } else {
            fprintf(out, "  handle=%p loop=%p type=%s gen=%lu needs_reinit=%d\n",
                    h->handle, h->loop, type_name, h->generation, h->needs_reinit);
        }
    }
    
    fprintf(out, "================================\n\n");
    
    pthread_mutex_unlock(&g_handle_mutex);
}

__attribute__((constructor(103)))
static void libuv_intercept_init(void) {
    if (nvsnap_self_disabled())
        return;

    /* Check if libuv interception is disabled */
    const char* disable_libuv = getenv("NVSNAP_DISABLE_LIBUV");
    if (disable_libuv && strcmp(disable_libuv, "1") == 0) {
        NVSNAP_DEBUG("libuv interception disabled via NVSNAP_DISABLE_LIBUV=1");
        return;
    }
    
    /* Check if lightweight mode - skip libuv/io_uring */
    const char* lightweight = getenv("NVSNAP_LIGHTWEIGHT");
    if (lightweight && strcmp(lightweight, "1") == 0) {
        NVSNAP_DEBUG("libuv interception disabled (lightweight mode)");
        return;
    }
    
    int uvloop = is_uvloop_loaded();
    
    /* Check if we're in a restored process - use file marker */
    g_libuv_restored = check_if_restored();
    
    NVSNAP_INFO("libuv intercept initialized (uvloop=%d, restored=%d, generation=%lu)", 
               uvloop, g_libuv_restored, g_generation);
    
    if (g_libuv_restored) {
        NVSNAP_INFO("Restore detected - handles will be reinited on first use");
        nvsnap_mark_handles_for_reinit();
    }
}

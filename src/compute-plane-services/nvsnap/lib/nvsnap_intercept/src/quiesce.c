/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NVSNAP Quiescence Coordinator
 *
 * This module coordinates pre-checkpoint quiescence for io_uring and libuv.
 * 
 * The problem:
 * - io_uring with SQPOLL creates kernel threads that CRIU can't checkpoint
 * - libuv/uvloop has C-level state that becomes stale after CRIU restore
 * - We need a generic solution that works with ANY container, no app changes
 *
 * Solution:
 * - LD_PRELOAD intercepts io_uring_setup(), libuv calls
 * - On SIGUSR1 (pre-checkpoint):
 *   1. Drain all io_uring rings (wait for pending I/O)
 *   2. Stop SQPOLL kernel threads
 *   3. Mark libuv loops for reinit after restore
 * - On restore detection (NVSNAP_RESTORED=1):
 *   1. Recreate io_uring instances (via transparent reinit in io_uring_intercept.c)
 *   2. Reinit libuv loops with uv_loop_fork()
 *   3. Handle invalidation for stale handles
 *
 * NOTE: io_uring tracking and reinit logic is in io_uring_intercept.c
 * This file handles libuv tracking and overall quiescence coordination.
 *
 * Build:
 *   Part of libnvsnap_intercept.so
 *
 * Usage:
 *   LD_PRELOAD=/opt/nvsnap/lib/libnvsnap_intercept.so your_app
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <signal.h>
#include <errno.h>
#include <pthread.h>
#include <sys/syscall.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <stdatomic.h>
#include <dlfcn.h>

#include "nvsnap_intercept.h"

/* io_uring syscall numbers */
#ifndef __NR_io_uring_enter
#define __NR_io_uring_enter 426
#endif

/* io_uring constants (from linux/io_uring.h) */
#define IORING_ENTER_GETEVENTS  (1U << 0)

/* Maximum tracked instances */
#define MAX_LIBUV_LOOPS 64

/*
 * =============================================================================
 * LIBUV TRACKING
 * =============================================================================
 */

typedef struct {
    void* loop;                 /* uv_loop_t pointer */
    int reinitialized;          /* Has uv_loop_fork() been called? */
    pthread_t owner_thread;
} nvsnap_libuv_loop_t;

static nvsnap_libuv_loop_t g_libuv_loops[MAX_LIBUV_LOOPS];
static int g_libuv_count = 0;
static pthread_mutex_t g_libuv_mutex = PTHREAD_MUTEX_INITIALIZER;

/*
 * =============================================================================
 * QUIESCENCE STATE
 * =============================================================================
 */

typedef enum {
    NVSNAP_QUIESCE_NONE = 0,
    NVSNAP_QUIESCE_REQUESTED,    /* SIGUSR1 received, quiesce requested */
    NVSNAP_QUIESCE_IN_PROGRESS,  /* One thread is performing quiescence */
    NVSNAP_QUIESCE_COMPLETE,     /* All I/O drained, safe to checkpoint */
    NVSNAP_QUIESCE_RESUMED,      /* SIGUSR2 received, resume normal operation */
} nvsnap_quiesce_state_t;

static atomic_int g_quiesce_state = NVSNAP_QUIESCE_NONE;
static _Atomic int g_is_restored = 0;
static int g_ack_pipe_fd = -1;
static int g_quiesce_pipe[2] = {-1, -1};
static int g_quiesce_meta_thread_started = 0;
static int g_quiesce_worker_thread_started = 0;


/* Forward declarations */
static void nvsnap_quiesce_libuv(void);
static void nvsnap_restore_libuv(void);
static void quiesce_signal_handler(int sig);
static void resume_signal_handler(int sig);
/* Metadata dump from io_uring_intercept.c */
void nvsnap_dump_uvloop_metadata(void);

/*
 * =============================================================================
 * SIGACTION GUARD
 * =============================================================================
 *
 * Python's asyncio.loop.add_signal_handler(SIGUSR1, ...) (called by uvicorn
 * during vLLM startup) replaces our C-level handler. We intercept sigaction()
 * to prevent this: save their handler for chaining, keep ours installed.
 *
 * Guard only active when NVSNAP_QUIESCE_SIGNALS=1 is set.
 * Restore pods do NOT set this, so CRIU is never affected.
 */
static int g_sigaction_guard_enabled = 0;
static struct sigaction g_saved_sigusr1 = { .sa_handler = SIG_DFL };
static struct sigaction g_saved_sigusr2 = { .sa_handler = SIG_DFL };
static int (*real_sigaction)(int, const struct sigaction *, struct sigaction *) = NULL;

static void resolve_real_sigaction(void) {
    /* Use __sigaction to get glibc's implementation directly.
     * We can't use dlvsym(RTLD_NEXT, "sigaction", "GLIBC_2.2.5") because
     * RTLD_NEXT resolves back to our own versioned symbol. */
    real_sigaction = (int (*)(int, const struct sigaction *, struct sigaction *))
        dlsym(RTLD_NEXT, "__sigaction");
    if (!real_sigaction)
        real_sigaction = (int (*)(int, const struct sigaction *, struct sigaction *))
            dlsym(RTLD_NEXT, "sigaction");
}

/*
 * Interpose sigaction with glibc version tag.
 *
 * Libraries linked against glibc (e.g. libtorch_cpu.so) reference the
 * versioned symbol sigaction@GLIBC_2.2.5. An unversioned LD_PRELOAD
 * override does NOT intercept versioned references — the dynamic linker
 * resolves them directly to glibc. We must export our override with the
 * same version tag to actually interpose these calls.
 */
__asm__(".symver nvsnap_sigaction,sigaction@@GLIBC_2.2.5");
int nvsnap_sigaction(int signum, const struct sigaction *act, struct sigaction *oldact) {
    if (!real_sigaction) {
        resolve_real_sigaction();
        if (!real_sigaction) { errno = EINVAL; return -1; }
    }
    if (act != NULL && (signum == SIGUSR1 || signum == SIGUSR2)) {
        if (g_sigaction_guard_enabled) {
            NVSNAP_INFO("sigaction guard BLOCKED override of %s (handler=%p, guard=1)",
                      signum == SIGUSR1 ? "SIGUSR1" : "SIGUSR2", (void *)act->sa_handler);
            if (signum == SIGUSR1) g_saved_sigusr1 = *act;
            else g_saved_sigusr2 = *act;
            if (oldact) return real_sigaction(signum, NULL, oldact);
            return 0;
        } else {
            NVSNAP_INFO("sigaction ALLOWING override of %s (handler=%p, guard=0)",
                      signum == SIGUSR1 ? "SIGUSR1" : "SIGUSR2", (void *)act->sa_handler);
        }
    }
    return real_sigaction(signum, act, oldact);
}

/*
 * Guard signal() the same way we guard sigaction().
 * Python's PyOS_setsig() may use signal() on some platforms,
 * which would bypass our sigaction() guard.
 */
static void (*(*real_signal)(int, void (*)(int)))(int) = NULL;

typedef void (*sighandler_t)(int);

__asm__(".symver nvsnap_signal,signal@@GLIBC_2.2.5");
sighandler_t nvsnap_signal(int signum, sighandler_t handler) {
    if (!real_signal) {
        /* Use bsd_signal to get glibc's implementation directly.
         * Can't use RTLD_NEXT on "signal" — resolves back to us. */
        real_signal = (sighandler_t (*)(int, sighandler_t))
            dlsym(RTLD_NEXT, "bsd_signal");
        if (!real_signal)
            real_signal = (sighandler_t (*)(int, sighandler_t))
                dlsym(RTLD_NEXT, "signal");
    }
    if (!real_signal) { errno = EINVAL; return SIG_ERR; }
    if (g_sigaction_guard_enabled && handler != SIG_ERR &&
        (signum == SIGUSR1 || signum == SIGUSR2)) {
        /* Save their handler, keep ours installed */
        if (signum == SIGUSR1) g_saved_sigusr1.sa_handler = handler;
        else g_saved_sigusr2.sa_handler = handler;
        return SIG_DFL;  /* Pretend old handler was default */
    }
    return real_signal(signum, handler);
}

static int install_signal_handler(int signum, void (*handler)(int)) {
    if (!real_sigaction) return -1;
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = handler;
    /* SA_RESTART for SIGUSR1: don't disrupt app syscalls (NCCL proxy threads,
     * Python I/O, etc). The quiesce worker thread polls every 50ms — that's
     * fast enough. No SA_RESTART for SIGUSR2: we WANT EINTR to wake threads
     * stuck in epoll_wait/poll after CRIU restore. */
    sa.sa_flags = (signum == SIGUSR1) ? SA_RESTART : 0;
    sigemptyset(&sa.sa_mask);
    return real_sigaction(signum, &sa, NULL);
}

/*
 * Quiesce worker thread: polls g_quiesce_state and performs quiescence.
 *
 * The SIGUSR1 signal handler only sets an atomic flag (async-signal-safe).
 * Something must poll that flag and do the actual work. For io_uring-using
 * processes, this happens inside io_uring_enter(). But NCCL worker processes
 * sit idle between requests and never call io_uring_enter(), so the flag
 * goes unnoticed. This thread ensures ALL processes perform quiescence
 * regardless of their I/O pattern.
 */
static void* quiesce_worker_thread(void* arg) {
    (void)arg;
    char trigger_path[64];
    snprintf(trigger_path, sizeof(trigger_path),
             "/dev/shm/nvsnap-quiesce-trigger-%d", getpid());

    for (;;) {
        /* File-based trigger: agent writes this file to request quiesce.
         * Content doesn't matter — it's just a trigger. The checkpoint path
         * is in a separate persistent file (/dev/shm/nvsnap-checkpoint-dir)
         * read at save time, avoiding any race with SIGUSR1. */
        if (access(trigger_path, F_OK) == 0) {
            NVSNAP_INFO("Quiesce trigger file detected: %s", trigger_path);
            unlink(trigger_path);

            int expected = NVSNAP_QUIESCE_NONE;
            atomic_compare_exchange_strong(&g_quiesce_state,
                    &expected, NVSNAP_QUIESCE_REQUESTED);
        }

        if (atomic_load(&g_quiesce_state) == NVSNAP_QUIESCE_REQUESTED) {
            nvsnap_perform_quiescence();
        }

        usleep(50000);  /* 50ms poll — file check doesn't need 1ms */
    }
    return NULL;
}

static void* quiesce_meta_thread(void* arg) {
    (void)arg;
    char buf[16];
    for (;;) {
        ssize_t n = read(g_quiesce_pipe[0], buf, sizeof(buf));
        if (n > 0) {
            nvsnap_dump_uvloop_metadata();
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        usleep(1000);
    }
    return NULL;
}

static void wake_quiesce_meta_thread(void) {
    if (g_quiesce_pipe[1] < 0) {
        return;
    }
    const char sig = 'Q';
    (void)write(g_quiesce_pipe[1], &sig, 1);
}

/*
 * =============================================================================
 * SIGNAL HANDLERS
 * =============================================================================
 */

/* 
 * Quiesce handler (SIGUSR1) - called before CRIU checkpoint
 * 
 * We can't do the actual quiescence work here because signal handlers
 * can only call async-signal-safe functions. Instead, we set a flag
 * and let the interception code handle it.
 */
static void quiesce_signal_handler(int sig) {
    (void)sig;

    /* Metadata-only mode: avoid full quiesce, just dump pointers. */
    const char* meta_only = getenv("NVSNAP_QUIESCE_METADATA_ONLY");
    if (meta_only && strcmp(meta_only, "1") == 0) {
        wake_quiesce_meta_thread();
        const char msg[] = "[NVSNAP] SIGUSR1: Metadata-only quiesce requested\n";
        (void)write(STDERR_FILENO, msg, sizeof(msg) - 1);
        return;
    }

    /* Set quiesce requested flag */
    atomic_store(&g_quiesce_state, NVSNAP_QUIESCE_REQUESTED);

    /* Trigger metadata dump thread */
    wake_quiesce_meta_thread();

    /* NOTE: Do NOT call nvsnap_zmq_handle_checkpoint() here.
     * zmq_ctx_checkpoint() modifies ZMQ state — if CRIU captures this,
     * the restored process has broken ZMQ IPC. ZMQ restore is handled
     * independently via zmq_reinit_all_if_restored(). */

    /* Notify via write (async-signal-safe) */
    const char msg[] = "[NVSNAP] SIGUSR1: Quiesce requested\n";
    (void)write(STDERR_FILENO, msg, sizeof(msg) - 1);

    /* Chain to saved handler (e.g., Python's asyncio handler) */
    if (g_saved_sigusr1.sa_handler != SIG_DFL &&
        g_saved_sigusr1.sa_handler != SIG_IGN &&
        g_saved_sigusr1.sa_handler != NULL) {
        g_saved_sigusr1.sa_handler(sig);
    }
}

/* Resume handler (SIGUSR2) - called after checkpoint or on cancel.
 * Must be minimal: set flag and return. No logging (can produce millions
 * of messages if signal is re-delivered), no chaining to unknown handlers
 * (can amplify signal delivery). */
static void resume_signal_handler(int sig) {
    (void)sig;
    atomic_store(&g_quiesce_state, NVSNAP_QUIESCE_RESUMED);
}

/*
 * =============================================================================
 * EXPORTED: Check if we're in a restored process
 * =============================================================================
 */

int nvsnap_is_restored(void) {
    return g_is_restored;
}

/*
 * =============================================================================
 * LIBUV TRACKING FUNCTIONS
 * =============================================================================
 */

/* Track a libuv loop */
int nvsnap_track_libuv_loop(void* loop) {
    if (!loop) return -1;
    
    pthread_mutex_lock(&g_libuv_mutex);
    
    /* Check if already tracked */
    for (int i = 0; i < g_libuv_count; i++) {
        if (g_libuv_loops[i].loop == loop) {
            pthread_mutex_unlock(&g_libuv_mutex);
            return 0;  /* Already tracked */
        }
    }
    
    if (g_libuv_count >= MAX_LIBUV_LOOPS) {
        NVSNAP_WARN("Too many libuv loops (%d)", g_libuv_count);
        pthread_mutex_unlock(&g_libuv_mutex);
        return -1;
    }
    
    g_libuv_loops[g_libuv_count].loop = loop;
    g_libuv_loops[g_libuv_count].reinitialized = 0;
    g_libuv_loops[g_libuv_count].owner_thread = pthread_self();
    g_libuv_count++;
    
    NVSNAP_DEBUG("Tracked libuv loop %p (total=%d)", loop, g_libuv_count);
    
    pthread_mutex_unlock(&g_libuv_mutex);
    return 0;
}

/* Check if loop needs reinit and reinit if needed */
int nvsnap_ensure_libuv_loop_ready(void* loop) {
    if (!g_is_restored || !loop) {
        return 0;  /* Not restored, nothing to do */
    }
    
    pthread_mutex_lock(&g_libuv_mutex);
    
    /* Find the loop */
    for (int i = 0; i < g_libuv_count; i++) {
        if (g_libuv_loops[i].loop == loop) {
            if (g_libuv_loops[i].reinitialized) {
                pthread_mutex_unlock(&g_libuv_mutex);
                return 0;  /* Already reinitialized */
            }
            
            /* Need to reinitialize */
            NVSNAP_INFO("Reinitializing libuv loop %p after CRIU restore", loop);
            
            /* Lookup real uv_loop_fork */
            static int (*real_uv_loop_fork)(void*) = NULL;
            if (!real_uv_loop_fork) {
                real_uv_loop_fork = dlsym(RTLD_DEFAULT, "uv_loop_fork");
            }
            
            if (real_uv_loop_fork) {
                int err = real_uv_loop_fork(loop);
                if (err != 0) {
                    NVSNAP_WARN("uv_loop_fork(%p) failed: %d", loop, err);
                } else {
                    NVSNAP_INFO("uv_loop_fork(%p) succeeded", loop);
                }
            } else {
                NVSNAP_WARN("uv_loop_fork not found, loop %p may be broken", loop);
            }
            
            g_libuv_loops[i].reinitialized = 1;
            pthread_mutex_unlock(&g_libuv_mutex);
            return 0;
        }
    }
    
    /* Not tracked yet, track and reinit */
    pthread_mutex_unlock(&g_libuv_mutex);
    nvsnap_track_libuv_loop(loop);
    
    /* Recursively ensure it's ready (will reinit on this call) */
    return nvsnap_ensure_libuv_loop_ready(loop);
}

/* Quiesce libuv (nothing to drain, just mark for reinit) */
static void nvsnap_quiesce_libuv(void) {
    pthread_mutex_lock(&g_libuv_mutex);
    
    NVSNAP_INFO("libuv quiesce: %d loops tracked", g_libuv_count);
    
    /* Mark all loops as needing reinit after restore */
    for (int i = 0; i < g_libuv_count; i++) {
        g_libuv_loops[i].reinitialized = 0;
    }
    
    pthread_mutex_unlock(&g_libuv_mutex);
}

/* 
 * External function from libuv_intercept.c (if enabled)
 * Weak symbol so we don't fail if libuv interception is disabled.
 */
__attribute__((weak)) void nvsnap_libuv_on_restore(void) {
    /* Stub - libuv interception disabled */
}

/* After restore: reset reinit flags so next use triggers uv_loop_fork */
static void nvsnap_restore_libuv(void) {
    g_is_restored = 1;

    pthread_mutex_lock(&g_libuv_mutex);

    NVSNAP_INFO("libuv restore: marking %d loops for reinit", g_libuv_count);

    for (int i = 0; i < g_libuv_count; i++) {
        g_libuv_loops[i].reinitialized = 0;
    }

    pthread_mutex_unlock(&g_libuv_mutex);

    /* Also mark all tracked handles for reinit */
    nvsnap_libuv_on_restore();

    /* Trigger ZMQ restore */
    NVSNAP_INFO("Triggering ZMQ restore");
    nvsnap_zmq_handle_restore();
}

/*
 * =============================================================================
 * MAIN QUIESCENCE FUNCTIONS
 * =============================================================================
 */

/*
 * Perform quiescence - called from interception points or polling thread
 * 
 * Returns: 1 if quiescence was performed, 0 if not needed
 */
int nvsnap_perform_quiescence(void) {
    /* Atomically transition REQUESTED → IN_PROGRESS. This ensures exactly one
     * thread performs quiescence even if both the worker thread and an
     * io_uring_enter() call race to check the flag. */
    int expected = NVSNAP_QUIESCE_REQUESTED;
    if (!atomic_compare_exchange_strong(&g_quiesce_state, &expected,
                                         NVSNAP_QUIESCE_IN_PROGRESS)) {
        return 0;
    }
    
    NVSNAP_INFO("=== Starting quiescence ===");
    
    /* 1. io_uring draining is handled by io_uring_intercept.c on io_uring_enter */
    NVSNAP_INFO("io_uring draining: handled by intercept layer");
    
    /* 2. Prepare libuv for restore */
    nvsnap_quiesce_libuv();

    /* 2.1 Capture uvloop loop pointers for restore */
    nvsnap_dump_uvloop_metadata();

    /* 2.2-2.3 Multi-GPU only: NCCL destroy + P2P disable + D2H save.
     * Single-GPU uses pure cuda-checkpoint (CRIU plugin handles everything).
     * The agent writes /dev/shm/nvsnap-multi-gpu for multi-GPU workloads. */
    if (access("/dev/shm/nvsnap-multi-gpu", F_OK) == 0) {
        NVSNAP_INFO("Multi-GPU detected — running NCCL/P2P/D2H quiesce");

        /* 2.2 NCCL destroy + P2P disable */
        {
            static int (*nvsnap_quiesce)(void) = NULL;
            static int quiesce_checked = 0;
            if (!quiesce_checked) {
                nvsnap_quiesce = (int (*)(void))dlsym(RTLD_DEFAULT,
                                                         "nvsnap_pre_checkpoint_quiesce");
                quiesce_checked = 1;
            }
            if (nvsnap_quiesce) {
                NVSNAP_INFO("NvSnap pre-checkpoint quiesce (NCCL destroy + P2P disable)");
                nvsnap_quiesce();
                NVSNAP_INFO("NvSnap pre-checkpoint quiesce done");
            }
        }

        /* 2.3 GPU D2H save */
        {
            static int (*nvsnap_save)(const char *) = NULL;
            static int nvsnap_checked = 0;
            if (!nvsnap_checked) {
                nvsnap_save = (int (*)(const char *))dlsym(RTLD_DEFAULT,
                                                              "nvsnap_checkpoint_save");
                nvsnap_checked = 1;
            }
            if (nvsnap_save) {
                char ckpt_dir[512] = {0};
                int pfd = open("/dev/shm/nvsnap-checkpoint-dir", O_RDONLY);
                if (pfd >= 0) {
                    ssize_t n = read(pfd, ckpt_dir, sizeof(ckpt_dir) - 1);
                    if (n > 0) ckpt_dir[n] = '\0';
                    close(pfd);
                }
                if (ckpt_dir[0]) {
                    mkdir(ckpt_dir, 0755);
                    NVSNAP_INFO("NvSnap GPU save: saving to %s", ckpt_dir);
                    int ret = nvsnap_save(ckpt_dir);
                    if (ret != 0)
                        NVSNAP_WARN("NvSnap GPU save failed: %d", ret);
                    else
                        NVSNAP_INFO("NvSnap GPU save: done");
                }
            }
        }
    } else {
        NVSNAP_INFO("Single-GPU — skipping NCCL/P2P/D2H (pure cuda-checkpoint)");
    }

    /* 2.5 Write per-PID quiesce done marker for agent polling */
    {
        char marker[256];
        snprintf(marker, sizeof(marker), "/dev/shm/nvsnap-quiesce-done-%d", getpid());
        int mfd = open(marker, O_CREAT | O_WRONLY, 0666);
        if (mfd >= 0) {
            (void)write(mfd, "done", 4);
            close(mfd);
            NVSNAP_INFO("Wrote quiesce done marker: %s", marker);
        } else {
            NVSNAP_WARN("Failed to write quiesce marker: %s", marker);
        }
    }

    /* 3. Mark complete */
    atomic_store(&g_quiesce_state, NVSNAP_QUIESCE_COMPLETE);
    
    /* 4. Send ACK to checkpoint agent */
    if (g_ack_pipe_fd >= 0) {
        char ack = 1;
        if (write(g_ack_pipe_fd, &ack, 1) != 1) {
            NVSNAP_WARN("Failed to send quiesce ACK");
        } else {
            NVSNAP_INFO("Quiesce ACK sent");
        }
    }
    
    NVSNAP_INFO("=== Quiescence complete ===");
    
    /* 5. Spin until checkpoint done or cancelled */
    NVSNAP_INFO("Waiting for checkpoint or resume signal...");
    while (atomic_load(&g_quiesce_state) == NVSNAP_QUIESCE_COMPLETE) {
        usleep(1000);  /* 1ms poll */
    }
    
    if (atomic_load(&g_quiesce_state) == NVSNAP_QUIESCE_RESUMED) {
        NVSNAP_INFO("Resumed after quiesce");
        atomic_store(&g_quiesce_state, NVSNAP_QUIESCE_NONE);
    }
    
    return 1;
}

/*
 * Perform post-restore reinitialization
 *
 * Called when NVSNAP_RESTORED=1 is detected
 */
void nvsnap_perform_restore_reinit(void) {
    static atomic_int reinit_done = 0;
    int expected = 0;
    if (!atomic_compare_exchange_strong(&reinit_done, &expected, 1)) return;

    NVSNAP_INFO("=== Starting post-restore reinitialization ===");
    
    /* 1. io_uring reinit is handled lazily in io_uring_intercept.c on first io_uring_enter */
    NVSNAP_INFO("io_uring restore: handled lazily by transparent reinit in intercept layer");
    
    /* 2. Mark libuv for reinit on first use */
    nvsnap_restore_libuv();

    /* 3-5. Multi-GPU only: NCCL restore + P2P re-enable + H2D restore.
     * Single-GPU: CRIU plugin Restore+Unlock handles everything. */
    if (access("/dev/shm/nvsnap-multi-gpu", F_OK) == 0) {
        NVSNAP_INFO("Multi-GPU restore path");

        /* 3. NCCL restore */
        nvsnap_nccl_restore();

        /* 4. GPU post-restore: re-enable P2P */
        {
            extern int nvsnap_gpu_post_restore(void);
            int p2p = nvsnap_gpu_post_restore();
            if (p2p > 0)
                NVSNAP_INFO("GPU post-restore: %d P2P pairs re-enabled", p2p);
        }
    } else {
        NVSNAP_INFO("Single-GPU — CRIU plugin handles GPU restore");
    }

    /* 5. GPU memory restore (multi-GPU only: reload D2H saved data).
     * Single-GPU: cuda-checkpoint already restored GPU memory. */
    if (access("/dev/shm/nvsnap-multi-gpu", F_OK) == 0) {
        static int (*nvsnap_restore)(const char *) = NULL;
        if (!nvsnap_restore)
            nvsnap_restore = (int (*)(const char *))dlsym(RTLD_DEFAULT,
                "nvsnap_checkpoint_restore_self");
        if (nvsnap_restore) {
            char ckpt_dir[512] = {0};
            int fd = open("/dev/shm/nvsnap-checkpoint-dir", O_RDONLY);
            if (fd >= 0) {
                ssize_t n = read(fd, ckpt_dir, sizeof(ckpt_dir) - 1);
                if (n > 0) ckpt_dir[n] = '\0';
                close(fd);
            }
            if (ckpt_dir[0]) {
                NVSNAP_INFO("GPU restore: reloading from %s (pid=%d)", ckpt_dir, getpid());
                int ret = nvsnap_restore(ckpt_dir);
                if (ret == 0)
                    NVSNAP_INFO("GPU restore: success (pid=%d)", getpid());
                else
                    NVSNAP_WARN("GPU restore: error %d (pid=%d)", ret, getpid());
            }
        }
    }

    NVSNAP_INFO("=== Post-restore reinitialization complete ===");
}

/*
 * =============================================================================
 * INITIALIZATION
 * =============================================================================
 */

static int g_quiesce_initialized = 0;

/* Start quiesce worker thread. Safe to call multiple times. */
void nvsnap_start_quiesce_worker(void) {
    if (!g_quiesce_worker_thread_started) {
        pthread_t wtid;
        if (pthread_create(&wtid, NULL, quiesce_worker_thread, NULL) == 0) {
            pthread_detach(wtid);
            g_quiesce_worker_thread_started = 1;
        }
    }
}

/* Called after fork() in child process. Restarts the quiesce worker thread
 * since threads don't survive fork(). Also resets quiesce state. */
/* Set in the fork child; the worker is created later from a normal context. */
static volatile sig_atomic_t g_quiesce_worker_needs_restart = 0;

/* Runs in the child after fork(). POSIX allows only async-signal-safe calls
 * here until exec(), so this may not create the worker thread directly even
 * though the child needs one (threads do not survive fork, and the trigger
 * file is keyed on the child's own pid).
 *
 * Creating it here deadlocks the child: pthread_create allocates TLS and takes
 * the loader lock, which a thread that did not survive the fork may hold.
 * That is not theoretical -- it wedges CRIU. CRIU forks during dump, and with
 * this library force-loaded into it via /etc/ld.so.preload the child hung
 * while the parent blocked in wait4 forever, stalling the dump before seize
 * completed. Isolating each behaviour showed installing signal handlers and
 * starting threads are both harmless; only this handler wedged it.
 *
 * So: record the need and let nvsnap_quiesce_worker_restart_if_needed() create
 * the thread from a safe context. Most forks are followed by exec, where the
 * constructors re-run and start the worker normally; this covers the
 * fork-without-exec case. */
static void nvsnap_quiesce_atfork_child(void)
{
    /* Reset state — child starts fresh */
    g_quiesce_state = NVSNAP_QUIESCE_NONE;
    g_quiesce_worker_thread_started = 0;
    g_quiesce_meta_thread_started = 0;

    g_quiesce_worker_needs_restart = 1;

    /* Reset NCCL tracking */
    nvsnap_nccl_atfork_child();
}

/* Create the worker deferred by the atfork handler. Safe to call from any
 * normal (non-atfork, non-signal) context; a no-op unless a fork left the
 * child without one. */
void nvsnap_quiesce_worker_restart_if_needed(void)
{
    if (!g_quiesce_worker_needs_restart)
        return;

    nvsnap_start_quiesce_worker();

    /* Drop the request only once a worker actually exists. pthread_create can
     * fail (EAGAIN under thread pressure, RLIMIT_NPROC), and clearing the flag
     * first would discard the request permanently, leaving a forked child with
     * no quiesce poller and no way to notice. Leaving it set means the next
     * caller retries. */
    if (g_quiesce_worker_thread_started)
        g_quiesce_worker_needs_restart = 0;
}

__attribute__((constructor(103)))  /* Run after main init (101) and NvSnap init (102) */
static void nvsnap_quiesce_register_atfork(void)
{
    if (nvsnap_self_disabled())
        return;

    /* Register fork handler:
     * child (after fork): reset quiesce state, restart worker thread,
     * clear NCCL tracking so children build fresh state. */
    pthread_atfork(NULL, NULL, nvsnap_quiesce_atfork_child);
}

void nvsnap_quiesce_init(void) {
    if (g_quiesce_initialized) return;
    
    /* Check if quiescence is disabled */
    const char* disable_quiesce = getenv("NVSNAP_DISABLE_QUIESCE");
    if (disable_quiesce && strcmp(disable_quiesce, "1") == 0) {
        g_quiesce_initialized = 1;
        return;
    }
    
    /* Check if lightweight mode - skip quiescence/io_uring */
    const char* lightweight = getenv("NVSNAP_LIGHTWEIGHT");
    if (lightweight && strcmp(lightweight, "1") == 0) {
        NVSNAP_DEBUG("Quiesce module disabled (lightweight mode)");
        g_quiesce_initialized = 1;
        return;
    }
    
    /* Check if we're a restored process */
    g_is_restored = (getenv("NVSNAP_RESTORED") != NULL);
    
    /* Get ACK pipe FD if provided */
    const char* ack_fd_str = getenv("NVSNAP_ACK_FD");
    if (ack_fd_str) {
        g_ack_pipe_fd = atoi(ack_fd_str);
    }
    
    /* 
     * NOTE: We do NOT install signal handlers by default anymore.
     * PyTorch's distributed module uses signals internally, and our handlers
     * can conflict. Only enable if NVSNAP_QUIESCE_SIGNALS=1 is set.
     */
    /* Resolve real sigaction for the guard pass-through */
    resolve_real_sigaction();

    const char* enable_signals = getenv("NVSNAP_QUIESCE_SIGNALS");
    if (enable_signals && strcmp(enable_signals, "1") == 0) {
        /* Install handlers via real_sigaction (bypasses our guard) */
        if (install_signal_handler(SIGUSR1, quiesce_signal_handler) == -1)
            NVSNAP_WARN("Failed to install SIGUSR1 quiesce handler: %s", strerror(errno));
        if (install_signal_handler(SIGUSR2, resume_signal_handler) == -1)
            NVSNAP_WARN("Failed to install SIGUSR2 resume handler: %s", strerror(errno));

        /* Enable guard — prevents asyncio from overriding our handlers */
        if (real_sigaction)
            g_sigaction_guard_enabled = 1;

        if (pipe(g_quiesce_pipe) == 0) {
            int flags = fcntl(g_quiesce_pipe[1], F_GETFL, 0);
            if (flags >= 0) {
                (void)fcntl(g_quiesce_pipe[1], F_SETFL, flags | O_NONBLOCK);
            }
            if (!g_quiesce_meta_thread_started) {
                pthread_t tid;
                if (pthread_create(&tid, NULL, quiesce_meta_thread, NULL) == 0) {
                    pthread_detach(tid);
                    g_quiesce_meta_thread_started = 1;
                    NVSNAP_INFO("Quiesce metadata thread started");
                } else {
                    NVSNAP_WARN("Failed to start quiesce metadata thread");
                }
            }
        } else {
            NVSNAP_WARN("Failed to create quiesce metadata pipe: %s", strerror(errno));
        }

        /* Start quiesce worker thread — ensures quiescence runs even in
         * processes that don't call io_uring_enter() (e.g. NCCL workers). */
        if (!g_quiesce_worker_thread_started) {
            pthread_t wtid;
            if (pthread_create(&wtid, NULL, quiesce_worker_thread, NULL) == 0) {
                pthread_detach(wtid);
                g_quiesce_worker_thread_started = 1;
                NVSNAP_INFO("Quiesce worker thread started");
            } else {
                NVSNAP_WARN("Failed to start quiesce worker thread");
            }
        }

        NVSNAP_INFO("Signal handlers installed for quiescence");
    }

    /* Register atfork handler to restart worker thread in children.
     * After fork(), only the calling thread survives — our worker thread
     * (which polls for quiesce triggers) is gone. Re-create it in child. */
    NVSNAP_INFO("Quiesce module initialized (restored=%d, ack_fd=%d)",
               g_is_restored, g_ack_pipe_fd);
    
    /* If restored, trigger reinit */
    if (g_is_restored) {
        NVSNAP_INFO("Detected CRIU restore, triggering reinitialization");
        nvsnap_perform_restore_reinit();
    }
    
    g_quiesce_initialized = 1;
}

/*
 * =============================================================================
 * DIAGNOSTIC FUNCTIONS
 * =============================================================================
 */

void nvsnap_dump_quiesce_state(FILE* out) {
    fprintf(out, "\n=== NVSNAP Quiesce State ===\n");
    fprintf(out, "Quiesce state: %d\n", atomic_load(&g_quiesce_state));
    fprintf(out, "Is restored: %d\n", g_is_restored);
    fprintf(out, "ACK pipe fd: %d\n", g_ack_pipe_fd);
    
    fprintf(out, "\nlibuv loops: %d\n", g_libuv_count);
    pthread_mutex_lock(&g_libuv_mutex);
    for (int i = 0; i < g_libuv_count; i++) {
        fprintf(out, "  loop=%p reinitialized=%d\n",
                g_libuv_loops[i].loop,
                g_libuv_loops[i].reinitialized);
    }
    pthread_mutex_unlock(&g_libuv_mutex);
    
    /* io_uring state is managed by io_uring_intercept.c */
    fprintf(out, "\nio_uring instances: see io_uring_intercept.c\n");
    
    fprintf(out, "===========================\n\n");
}

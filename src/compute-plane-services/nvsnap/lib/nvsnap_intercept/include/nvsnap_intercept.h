/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NVSNAP Interception Library
 * 
 * This library intercepts io_uring and libuv for checkpoint/restore support.
 * 
 * Key features:
 * - io_uring: Tracks and drains rings before checkpoint, recreates after restore
 * - libuv: Tracks loops and reinitializes handles after restore
 * 
 * Note: GPU/CUDA state is handled externally by cuda-checkpoint (NVIDIA's tool).
 * This library focuses on process-level I/O subsystems that CRIU can't handle natively.
 */

#ifndef NVSNAP_INTERCEPT_H
#define NVSNAP_INTERCEPT_H

#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include <pthread.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

/*
 * =============================================================================
 * CONFIGURATION
 * =============================================================================
 */

/* Environment variables */
#define NVSNAP_ENV_LOG_LEVEL     "NVSNAP_LOG_LEVEL"      /* 0=off, 1=error, 2=warn, 3=info, 4=debug, 5=trace */
#define NVSNAP_ENV_LOG_FILE      "NVSNAP_LOG_FILE"       /* Path to log file, or "stderr" */
#define NVSNAP_ENV_ENABLED       "NVSNAP_ENABLED"        /* 0 to disable interception */

/* Restore detection marker files */
#define CRIU_RESTORE_MARKER     "/run/criu-restored"           /* Generic - preferred */
#define NVSNAP_RESTORE_MARKER    "/var/run/nvsnap/.restored"     /* Legacy */

/*
 * =============================================================================
 * LOGGING
 * =============================================================================
 */

typedef enum {
    NVSNAP_LOG_OFF = 0,
    NVSNAP_LOG_ERROR = 1,
    NVSNAP_LOG_WARN = 2,
    NVSNAP_LOG_INFO = 3,
    NVSNAP_LOG_DEBUG = 4,
    NVSNAP_LOG_TRACE = 5,
} nvsnap_log_level_t;

void nvsnap_log(nvsnap_log_level_t level, const char* func, const char* fmt, ...);

#define NVSNAP_ERROR(fmt, ...) nvsnap_log(NVSNAP_LOG_ERROR, __func__, fmt, ##__VA_ARGS__)
#define NVSNAP_WARN(fmt, ...)  nvsnap_log(NVSNAP_LOG_WARN, __func__, fmt, ##__VA_ARGS__)
#define NVSNAP_INFO(fmt, ...)  nvsnap_log(NVSNAP_LOG_INFO, __func__, fmt, ##__VA_ARGS__)
#define NVSNAP_DEBUG(fmt, ...) nvsnap_log(NVSNAP_LOG_DEBUG, __func__, fmt, ##__VA_ARGS__)
#define NVSNAP_TRACE(fmt, ...) nvsnap_log(NVSNAP_LOG_TRACE, __func__, fmt, ##__VA_ARGS__)

/*
 * =============================================================================
 * GLOBAL STATE
 * =============================================================================
 */

typedef struct nvsnap_state {
    /* Initialization */
    bool initialized;
    bool enabled;
    pthread_mutex_t init_mutex;
    
    /* Logging */
    nvsnap_log_level_t log_level;
    FILE* log_file;
    pthread_mutex_t log_mutex;
    
} nvsnap_state_t;

/* Global state accessor */
nvsnap_state_t* nvsnap_get_state(void);

/*
 * =============================================================================
 * INITIALIZATION
 * =============================================================================
 */

/* Called automatically via __attribute__((constructor)) */
void nvsnap_init(void);
void nvsnap_fini(void);

/* Manual initialization (for testing) */
int nvsnap_init_explicit(void);

/*
 * =============================================================================
 * QUIESCENCE API (io_uring and libuv)
 * =============================================================================
 * 
 * These functions are used to track and manage io_uring and libuv instances
 * for checkpoint/restore. They are called automatically by our intercepted
 * functions, but can also be called manually for debugging.
 */

/* Track an io_uring instance */
int nvsnap_track_io_uring(int fd, uint32_t sq_entries, uint32_t cq_entries, 
                          uint32_t flags);
int nvsnap_untrack_io_uring(int fd);

/* Update mmap addresses for a tracked io_uring (call after mmap) */
int nvsnap_update_io_uring_addrs(int fd);

/* Check if io_uring needs reinit after restore */
int nvsnap_io_uring_needs_reinit(int fd);

/* Mark io_uring as validated after restore */
void nvsnap_mark_io_uring_validated(int fd);

/* Track a libuv loop */
int nvsnap_track_libuv_loop(void* loop);
int nvsnap_ensure_libuv_loop_ready(void* loop);

/* Perform quiescence (drain io_uring, prepare libuv) - called before checkpoint */
int nvsnap_perform_quiescence(void);

/* Perform post-restore reinitialization */
void nvsnap_perform_restore_reinit(void);

/* Dump quiesce state for debugging */
void nvsnap_dump_quiesce_state(FILE* out);

/* No-op stubs: uvloop handles uv_loop_fork() natively now (checkpoint-restore-v1 patch) */
void nvsnap_dump_uvloop_metadata(void);
void nvsnap_install_uvloop_hook_async(void);

/*
 * =============================================================================
 * ZMQ INTERCEPTION
 * =============================================================================
 */

/* Reinitialize ZMQ contexts if a restore is detected */
void nvsnap_zmq_reinit_all_if_restored(void);

/*
 * =============================================================================
 * LIBUV INTERCEPTION
 * =============================================================================
 * 
 * Enable libuv interception after restore is detected.
 * This allows us to call uv_loop_fork() on restored loops.
 */

void nvsnap_libuv_enable_interception(void);

/*
 * =============================================================================
 * SECCOMP-BPF INTERCEPTION
 * =============================================================================
 * 
 * Use seccomp-bpf to intercept io_uring syscalls at the kernel boundary.
 * This works regardless of static/dynamic linking (critical for uvloop).
 * 
 * Environment variables:
 *   NVSNAP_SECCOMP_ENABLED=1  - Enable seccomp interception (default: 0)
 *   NVSNAP_POST_RESTORE=1     - Mark as post-restore for healing
 */

#define NVSNAP_ENV_SECCOMP_ENABLED  "NVSNAP_SECCOMP_ENABLED"
#define NVSNAP_ENV_POST_RESTORE     "NVSNAP_POST_RESTORE"

/* Install seccomp-bpf filter to trap io_uring syscalls */
int nvsnap_seccomp_install_filter(void);

/* Enable post-restore mode - io_uring calls may need healing */
void nvsnap_seccomp_set_post_restore(bool post_restore);

/* Check if seccomp is installed */
bool nvsnap_seccomp_is_installed(void);

/* Get statistics */
void nvsnap_seccomp_get_stats(int* enter_count, int* heal_count);

/*
 * =============================================================================
 * NCCL INTERCEPTION
 * =============================================================================
 */

void nvsnap_nccl_quiesce(void);
void nvsnap_nccl_restore(void);
void nvsnap_nccl_atfork_child(void);
void *nvsnap_nccl_symbol_override(const char *symbol);

/*
 * =============================================================================
 * CUDA MEMORY INTERCEPTION
 * =============================================================================
 *
 * Tracks GPU allocations for multi-GPU checkpoint/restore without cuda-checkpoint.
 * Enable: NVSNAP_CUDA_INTERCEPT=1
 */

/* Save all live GPU allocations to files in dir (D2H + manifest JSON) */
int nvsnap_cuda_save(const char *dir);

/* dlsym override for cuMemAlloc_v2/cuMemFree_v2 interception */
void *nvsnap_cuda_symbol_override(const char *symbol);

#ifdef __cplusplus
}
#endif

/* ZMQ checkpoint/restore support */
void nvsnap_zmq_handle_checkpoint(void);
void nvsnap_zmq_handle_restore(void);

/* Non-zero when this library must stay completely inert in the current
 * process, because /etc/ld.so.preload force-loaded it into one of our own
 * bundle binaries (CRIU and friends). Every constructor must check this
 * first. See src/self_disable.c. */
int nvsnap_self_disabled(void);

/* Creates the quiesce worker a fork() child could not create in its atfork
 * handler (pthread_create is not async-signal-safe and deadlocks there).
 * No-op unless a fork left this process without one. */
void nvsnap_quiesce_worker_restart_if_needed(void);

#endif /* NVSNAP_INTERCEPT_H */

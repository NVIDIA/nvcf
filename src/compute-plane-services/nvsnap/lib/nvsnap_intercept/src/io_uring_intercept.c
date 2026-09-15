/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * io_uring Interception for NVSNAP
 *
 * This module intercepts io_uring syscalls to track all io_uring instances.
 * On quiesce signal, we can drain all rings before CRIU checkpoint.
 *
 * Why intercept at syscall level:
 * - io_uring has no library - apps use raw syscalls or liburing
 * - We can't dlsym a syscall
 * - Solution: intercept via libc's syscall() function wrapper
 *
 * TRANSPARENT REINIT ARCHITECTURE:
 * After CRIU restore, io_uring fds may be invalidated (e.g., overwritten by BPF).
 * Instead of debugging WHO breaks the fd, we DETECT and FIX it transparently:
 *
 * 1. On first io_uring_enter after restore, verify fd is still io_uring
 * 2. If not (EBADF or wrong fd type), transparently recreate:
 *    - Create new io_uring with saved params
 *    - Close whatever is at the expected fd
 *    - dup2 new io_uring to expected fd
 *    - mmap ring memory at the same addresses app expects
 *    - Sync ring indices
 * 3. Continue - app never knows anything happened
 *
 * This is GENERIC - works for any application using io_uring without modification.
 *
 * NOTE: uvloop/libuv kernel state reinit (uv_loop_fork) is handled natively
 * by the patched uvloop, not by this module. See uvloop checkpoint-restore-v1.
 *
 * Build:
 *   Part of libnvsnap_intercept.so
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <stdarg.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <dlfcn.h>
#include <fcntl.h>
#include <sys/syscall.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <execinfo.h>  /* for backtrace() - debugging */
#include <pthread.h>
#include <stdatomic.h>

#include "nvsnap_intercept.h"

/* io_uring syscall numbers */
#ifndef __NR_io_uring_setup
#define __NR_io_uring_setup 425
#endif
#ifndef __NR_io_uring_enter
#define __NR_io_uring_enter 426
#endif
#ifndef __NR_io_uring_register
#define __NR_io_uring_register 427
#endif

/* io_uring setup flags */
#define IORING_SETUP_SQPOLL     (1U << 1)
#define IORING_SETUP_SQE128     (1U << 10)
#define IORING_SETUP_CQE32      (1U << 11)

/* io_uring features */
#define IORING_FEAT_SINGLE_MMAP (1U << 0)

/* io_uring enter flags */
#define IORING_ENTER_GETEVENTS  (1U << 0)
#define IORING_ENTER_SQ_WAKEUP  (1U << 1)

/* io_uring sq flags */
#define IORING_SQ_NEED_WAKEUP   (1U << 0)

/* mmap offsets for io_uring */
#define IORING_OFF_SQ_RING 0ULL
#define IORING_OFF_CQ_RING 0x8000000ULL
#define IORING_OFF_SQES    0x10000000ULL

/* External functions from quiesce.c */
extern int nvsnap_is_restored(void);
extern int nvsnap_perform_quiescence(void);

/* Real syscall function */
static long (*real_syscall)(long number, ...) = NULL;

/* Real mmap and close functions - needed for reinit to avoid recursive interception */
typedef void* (*mmap_fn)(void *addr, size_t length, int prot, int flags, int fd, off_t offset);
typedef int (*close_fn)(int fd);
static mmap_fn real_mmap = NULL;
static close_fn real_close = NULL;

/* Restore state tracking
 * Cross-thread flags use _Atomic int to avoid undefined behavior.
 */
static _Atomic int g_restore_checked = 0;
static _Atomic int g_is_restored = 0;
/* g_io_uring_calls_since_restore removed - was used by uvloop fork tracking */

/* Config values: only written once during init or after restore reset, read-mostly */
static int g_disable_io_uring_reinit = -1;
static int g_debug_io_uring = -1;

/* Generic CRIU restore marker - used by all patched components */
#define CRIU_RESTORE_MARKER "/run/criu-restored"

/* Legacy NVSNAP-specific markers (for backwards compatibility) */
#define NVSNAP_RESTORE_MARKER "/var/run/nvsnap/.restored"

/*
 * =============================================================================
 * IO_URING TRACKING WITH FULL PARAMS FOR TRANSPARENT REINIT
 * =============================================================================
 */

/*
 * io_uring_params structure (from linux/io_uring.h)
 * Full definition for capturing setup params
 */
struct io_uring_params {
    uint32_t sq_entries;
    uint32_t cq_entries;
    uint32_t flags;
    uint32_t sq_thread_cpu;
    uint32_t sq_thread_idle;
    uint32_t features;
    uint32_t wq_fd;
    uint32_t resv[3];
    struct io_sqring_offsets {
        uint32_t head;
        uint32_t tail;
        uint32_t ring_mask;
        uint32_t ring_entries;
        uint32_t flags;
        uint32_t dropped;
        uint32_t array;
        uint32_t resv1;
        uint64_t resv2;
    } sq_off;
    struct io_cqring_offsets {
        uint32_t head;
        uint32_t tail;
        uint32_t ring_mask;
        uint32_t ring_entries;
        uint32_t overflow;
        uint32_t cqes;
        uint32_t flags;
        uint32_t resv1;
        uint64_t resv2;
    } cq_off;
};

/* Maximum tracked instances */
#define MAX_IO_URING_INSTANCES 256

/*
 * Extended tracking structure with all info needed for reinit
 */
typedef struct {
    int fd;                          /* io_uring file descriptor */
    int active;                      /* Is this slot in use? */
    int validated_after_restore;     /* Has been verified working post-restore */
    int reinit_succeeded;            /* Did reinit succeed? */
    int reinit_attempts;             /* How many reinit attempts (max 3) */
    int invalidated_after_restore;   /* Have we invalidated this fd post-restore */

    /* Setup params - needed to recreate */
    uint32_t setup_entries;          /* entries arg to io_uring_setup */
    struct io_uring_params params;   /* Full params returned by kernel */

    /* Memory mapping addresses - needed to remap at same locations */
    unsigned long sq_ring_addr;
    unsigned long cq_ring_addr;      /* May be same as sq_ring_addr if SINGLE_MMAP */
    unsigned long sqe_addr;
    size_t sq_ring_size;
    size_t cq_ring_size;
    size_t sqe_size;

    /* Ring indices at last known good state */
    uint32_t sq_head;
    uint32_t sq_tail;
    uint32_t cq_head;
    uint32_t cq_tail;

    pthread_t owner_thread;
} nvsnap_io_uring_instance_t;

static nvsnap_io_uring_instance_t g_io_urings[MAX_IO_URING_INSTANCES];
static int g_io_uring_count = 0;
static pthread_mutex_t g_io_uring_mutex = PTHREAD_MUTEX_INITIALIZER;

/* Forward declarations for state flags defined later */
static _Atomic int g_proactive_reinit_done;
static _Atomic int g_pre_restore_check_done;

/* Check if we're in a restored process.
 * IMPORTANT: We ALWAYS check the marker file because CRIU preserves
 * memory state from before checkpoint, including all state flags.
 * So we cannot rely on a one-time check; we must check on every call
 * until we detect restore.
 *
 * When restore is detected, we also RESET all state flags to ensure
 * the reinit logic runs.
 */
static int check_restored(void) {
    /* If already detected as restored, return immediately */
    if (g_is_restored) return 1;

    int detected = 0;

    /* Check generic CRIU restore marker (preferred) */
    if (access(CRIU_RESTORE_MARKER, F_OK) == 0) {
        NVSNAP_WARN("=== RESTORE DETECTED via %s ===", CRIU_RESTORE_MARKER);
        detected = 1;
    }
    /* Check legacy NVSNAP-specific markers */
    else if (access("/nvsnap-lib/.restored", F_OK) == 0) {
        NVSNAP_WARN("=== RESTORE DETECTED via /nvsnap-lib/.restored ===");
        detected = 1;
    } else if (access("/nvsnap/.restored", F_OK) == 0) {
        NVSNAP_WARN("=== RESTORE DETECTED via /nvsnap/.restored ===");
        detected = 1;
    } else if (access(NVSNAP_RESTORE_MARKER, F_OK) == 0) {
        NVSNAP_WARN("=== RESTORE DETECTED via %s ===", NVSNAP_RESTORE_MARKER);
        detected = 1;
    }

    /* Check environment variable */
    if (!detected && getenv("NVSNAP_RESTORED")) {
        NVSNAP_WARN("=== RESTORE DETECTED via NVSNAP_RESTORED env ===");
        detected = 1;
    }

    /* Check via quiesce.c export */
    if (!detected && nvsnap_is_restored()) {
        NVSNAP_WARN("=== RESTORE DETECTED via quiesce ===");
        detected = 1;
    }

    if (detected) {
        g_is_restored = 1;
        /* CRITICAL: Reset all state flags that were preserved from checkpoint.
         * These flags would prevent reinit logic from running. */
        g_proactive_reinit_done = 0;
        g_pre_restore_check_done = 0;
        g_restore_checked = 0;
        g_debug_io_uring = -1;
        g_disable_io_uring_reinit = -1;
        NVSNAP_WARN("Reset state flags: proactive_reinit_done=0, pre_restore_check_done=0");
        nvsnap_zmq_reinit_all_if_restored();
        return 1;
    }

    return 0;
}

static int is_debug_io_uring_enabled(void) {
    if (g_debug_io_uring >= 0) {
        return g_debug_io_uring;
    }
    const char *env = getenv("NVSNAP_DEBUG_IO_URING");
    if (env && (!strcmp(env, "1") || !strcasecmp(env, "true") || !strcasecmp(env, "yes"))) {
        g_debug_io_uring = 1;
        return 1;
    }
    if (access("/nvsnap-lib/.debug_io_uring", F_OK) == 0) {
        g_debug_io_uring = 1;
        return 1;
    }
    g_debug_io_uring = 0;
    return 0;
}

static int is_io_uring_reinit_disabled(void) {
    if (g_disable_io_uring_reinit >= 0) {
        return g_disable_io_uring_reinit;
    }
    const char *env = getenv("NVSNAP_DISABLE_IO_URING_REINIT");
    if (env && (!strcmp(env, "1") || !strcasecmp(env, "true") || !strcasecmp(env, "yes"))) {
        g_disable_io_uring_reinit = 1;
        return 1;
    }
    if (access("/nvsnap-lib/.disable_io_uring_reinit", F_OK) == 0) {
        g_disable_io_uring_reinit = 1;
        return 1;
    }
    g_disable_io_uring_reinit = 0;
    return 0;
}

/*
 * Initialize real syscall pointer
 */
static void init_real_syscall(void) {
    if (!real_syscall) {
        real_syscall = dlsym(RTLD_NEXT, "syscall");
        if (!real_syscall) {
            /* Fallback to libc */
            real_syscall = dlsym(RTLD_DEFAULT, "syscall");
        }
    }
}

/*
 * =============================================================================
 * FD VERIFICATION AND INFO PARSING
 * =============================================================================
 */

/* Verify an fd is actually an io_uring by checking /proc/self/fd link */
static int is_valid_io_uring_fd(int fd) {
    char link_path[64];
    char target[256];

    snprintf(link_path, sizeof(link_path), "/proc/self/fd/%d", fd);
    ssize_t len = readlink(link_path, target, sizeof(target) - 1);

    if (len <= 0) {
        return 0;  /* fd doesn't exist */
    }
    target[len] = '\0';

    /* Check if it's an io_uring */
    return (strstr(target, "io_uring") != NULL);
}

/* Get what an fd points to */
static int get_fd_type(int fd, char *buf, size_t buflen) {
    char link_path[64];
    snprintf(link_path, sizeof(link_path), "/proc/self/fd/%d", fd);
    ssize_t len = readlink(link_path, buf, buflen - 1);
    if (len > 0) {
        buf[len] = '\0';
        return 0;
    }
    return -1;
}

/* Parse io_uring params from fdinfo */
static int parse_io_uring_fdinfo(int fd, uint32_t *sq_entries, uint32_t *cq_entries,
                                  uint32_t *sq_head, uint32_t *sq_tail,
                                  uint32_t *cq_head, uint32_t *cq_tail) {
    char fdinfo_path[64];
    char line[256];
    snprintf(fdinfo_path, sizeof(fdinfo_path), "/proc/self/fdinfo/%d", fd);

    FILE *f = fopen(fdinfo_path, "r");
    if (!f) return 0;

    int is_io_uring = 0;
    *sq_entries = 0; *cq_entries = 0;
    *sq_head = 0; *sq_tail = 0; *cq_head = 0; *cq_tail = 0;

    while (fgets(line, sizeof(line), f)) {
        unsigned int val;
        if (strncmp(line, "SqMask:", 7) == 0) {
            is_io_uring = 1;
            if (sscanf(line, "SqMask: %u", &val) == 1) {
                *sq_entries = val + 1;
            }
        } else if (strncmp(line, "CqMask:", 7) == 0) {
            if (sscanf(line, "CqMask: %u", &val) == 1) {
                *cq_entries = val + 1;
            }
        } else if (strncmp(line, "SqHead:", 7) == 0) {
            sscanf(line, "SqHead: %u", sq_head);
        } else if (strncmp(line, "SqTail:", 7) == 0) {
            sscanf(line, "SqTail: %u", sq_tail);
        } else if (strncmp(line, "CqHead:", 7) == 0) {
            sscanf(line, "CqHead: %u", cq_head);
        } else if (strncmp(line, "CqTail:", 7) == 0) {
            sscanf(line, "CqTail: %u", cq_tail);
        }
    }

    fclose(f);
    return is_io_uring;
}

static void dump_io_uring_fdinfo(int fd, const char *tag) {
    uint32_t sq_entries = 0;
    uint32_t cq_entries = 0;
    uint32_t sq_head = 0;
    uint32_t sq_tail = 0;
    uint32_t cq_head = 0;
    uint32_t cq_tail = 0;

    int is_ring = parse_io_uring_fdinfo(fd, &sq_entries, &cq_entries,
                                        &sq_head, &sq_tail, &cq_head, &cq_tail);
    if (!is_ring) {
        NVSNAP_WARN("[%s] fd=%d is not io_uring or fdinfo missing", tag, fd);
        return;
    }

    NVSNAP_WARN("[%s] fd=%d sq_entries=%u cq_entries=%u sq=%u/%u cq=%u/%u",
               tag, fd, sq_entries, cq_entries, sq_head, sq_tail, cq_head, cq_tail);
}

/* Parse io_uring mmap addresses from /proc/self/maps */
static void capture_io_uring_mmap_addrs(int fd, nvsnap_io_uring_instance_t *inst) {
    char line[512];
    char fd_path[64];
    char fd_link[256];

    snprintf(fd_path, sizeof(fd_path), "/proc/self/fd/%d", fd);
    ssize_t link_len = readlink(fd_path, fd_link, sizeof(fd_link) - 1);
    if (link_len <= 0) return;
    fd_link[link_len] = '\0';

    FILE *f = fopen("/proc/self/maps", "r");
    if (!f) return;

    while (fgets(line, sizeof(line), f)) {
        if (strstr(line, fd_link) || strstr(line, "io_uring")) {
            unsigned long start, end, offset;
            char perms[8];

            if (sscanf(line, "%lx-%lx %7s %lx", &start, &end, perms, &offset) >= 4) {
                size_t size = end - start;

                /* Identify which mapping this is based on offset */
                if (offset == IORING_OFF_SQ_RING || offset == 0) {
                    if (inst->sq_ring_addr == 0) {
                        inst->sq_ring_addr = start;
                        inst->sq_ring_size = size;
                        NVSNAP_DEBUG("  Captured SQ ring: addr=0x%lx size=%zu", start, size);
                    }
                } else if (offset == IORING_OFF_CQ_RING || offset == 0x8000000) {
                    if (inst->cq_ring_addr == 0 || inst->cq_ring_addr == inst->sq_ring_addr) {
                        inst->cq_ring_addr = start;
                        inst->cq_ring_size = size;
                        NVSNAP_DEBUG("  Captured CQ ring: addr=0x%lx size=%zu", start, size);
                    }
                } else if (offset == IORING_OFF_SQES || offset == 0x10000000) {
                    if (inst->sqe_addr == 0) {
                        inst->sqe_addr = start;
                        inst->sqe_size = size;
                        NVSNAP_DEBUG("  Captured SQEs: addr=0x%lx size=%zu", start, size);
                    }
                }
            }
        }
    }

    fclose(f);
}

/*
 * =============================================================================
 * TRANSPARENT IO_URING REINIT
 * =============================================================================
 */

/* Page-align a size */
static inline unsigned long align_up(unsigned long size, unsigned long align) {
    return (size + align - 1) & ~(align - 1);
}

/*
 * Transparently recreate an io_uring at the expected fd and mmap addresses.
 * This is the core of the self-healing architecture.
 */
static int reinit_io_uring_at_fd(nvsnap_io_uring_instance_t *inst) {
    init_real_syscall();

    NVSNAP_WARN("=== TRANSPARENT IO_URING REINIT for fd=%d ===", inst->fd);
    NVSNAP_INFO("  Original params: entries=%u flags=0x%x",
               inst->setup_entries, inst->params.flags);
    NVSNAP_INFO("  Saved addrs: sq=0x%lx cq=0x%lx sqe=0x%lx",
               inst->sq_ring_addr, inst->cq_ring_addr, inst->sqe_addr);

    /* Prepare params for new io_uring - strip SQPOLL for restore compatibility */
    struct io_uring_params new_params = {0};
    new_params.flags = inst->params.flags & ~IORING_SETUP_SQPOLL;  /* Strip SQPOLL */

    /* Ensure we have real_close for cleanup */
    if (!real_close) {
        real_close = dlsym(RTLD_NEXT, "close");
    }

    /* Create new io_uring */
    int new_fd = real_syscall(__NR_io_uring_setup, inst->setup_entries, &new_params);
    if (new_fd < 0) {
        NVSNAP_WARN("  io_uring_setup failed: %s", strerror(errno));
        return -1;
    }

    NVSNAP_INFO("  Created new io_uring: fd=%d sq=%u cq=%u features=0x%x",
               new_fd, new_params.sq_entries, new_params.cq_entries, new_params.features);

    int single_mmap = (new_params.features & IORING_FEAT_SINGLE_MMAP) != 0;

    /* Calculate mmap sizes from kernel params */
    size_t sq_ring_size = align_up(new_params.sq_off.array +
                                    new_params.sq_entries * sizeof(uint32_t), 4096);
    size_t sqe_size = align_up(new_params.sq_entries * 64, 4096);  /* sizeof(io_uring_sqe) = 64 */
    size_t cq_ring_size = align_up(new_params.cq_off.cqes +
                                    new_params.cq_entries * 16, 4096);  /* sizeof(io_uring_cqe) = 16 */

    if (single_mmap && cq_ring_size > sq_ring_size) {
        sq_ring_size = cq_ring_size;
    }

    /*
     * Map rings using real_mmap to avoid recursive interception!
     */
    if (!real_mmap) {
        real_mmap = dlsym(RTLD_NEXT, "mmap");
    }

    /* Map SQ ring at the original address */
    if (inst->sq_ring_addr) {
        /* First unmap anything at target address */
        munmap((void *)inst->sq_ring_addr, inst->sq_ring_size > 0 ? inst->sq_ring_size : sq_ring_size);

        void *sq_ptr = real_mmap((void *)inst->sq_ring_addr, sq_ring_size,
                                  PROT_READ | PROT_WRITE, MAP_SHARED | MAP_FIXED,
                                  new_fd, IORING_OFF_SQ_RING);
        if (sq_ptr == MAP_FAILED) {
            NVSNAP_WARN("  Failed to mmap SQ ring at 0x%lx: %s",
                       inst->sq_ring_addr, strerror(errno));
            real_close(new_fd);
            return -1;
        }
        NVSNAP_INFO("  Mapped SQ ring at 0x%lx (size=%zu)", inst->sq_ring_addr, sq_ring_size);

        /* Map CQ ring if not single_mmap */
        if (!single_mmap && inst->cq_ring_addr && inst->cq_ring_addr != inst->sq_ring_addr) {
            munmap((void *)inst->cq_ring_addr, inst->cq_ring_size > 0 ? inst->cq_ring_size : cq_ring_size);

            void *cq_ptr = real_mmap((void *)inst->cq_ring_addr, cq_ring_size,
                                      PROT_READ | PROT_WRITE, MAP_SHARED | MAP_FIXED,
                                      new_fd, IORING_OFF_CQ_RING);
            if (cq_ptr == MAP_FAILED) {
                NVSNAP_WARN("  Failed to mmap CQ ring at 0x%lx: %s",
                           inst->cq_ring_addr, strerror(errno));
                /* Non-fatal - continue */
            } else {
                NVSNAP_INFO("  Mapped CQ ring at 0x%lx (size=%zu)", inst->cq_ring_addr, cq_ring_size);
            }
        }
    }

    /* Map SQEs at the original address */
    if (inst->sqe_addr) {
        munmap((void *)inst->sqe_addr, inst->sqe_size > 0 ? inst->sqe_size : sqe_size);

        void *sqe_ptr = real_mmap((void *)inst->sqe_addr, sqe_size,
                                   PROT_READ | PROT_WRITE, MAP_SHARED | MAP_FIXED,
                                   new_fd, IORING_OFF_SQES);
        if (sqe_ptr == MAP_FAILED) {
            NVSNAP_WARN("  Failed to mmap SQEs at 0x%lx: %s",
                       inst->sqe_addr, strerror(errno));
            real_close(new_fd);
            return -1;
        }
        NVSNAP_INFO("  Mapped SQEs at 0x%lx (size=%zu)", inst->sqe_addr, sqe_size);
    }

    /* Sync ring indices */
    if (inst->sq_ring_addr) {
        unsigned long cq_base = single_mmap ? inst->sq_ring_addr :
                                (inst->cq_ring_addr ? inst->cq_ring_addr : inst->sq_ring_addr);

        volatile uint32_t *sq_head = (uint32_t *)((char *)inst->sq_ring_addr + new_params.sq_off.head);
        volatile uint32_t *sq_tail = (uint32_t *)((char *)inst->sq_ring_addr + new_params.sq_off.tail);
        volatile uint32_t *cq_head = (uint32_t *)((char *)cq_base + new_params.cq_off.head);
        volatile uint32_t *cq_tail = (uint32_t *)((char *)cq_base + new_params.cq_off.tail);
        volatile uint32_t *sqflags = (uint32_t *)((char *)inst->sq_ring_addr + new_params.sq_off.flags);

        /* Sync to saved indices (make rings appear empty at app's expected position) */
        *sq_head = inst->sq_tail;
        *sq_tail = inst->sq_tail;
        *cq_head = inst->cq_head;
        *cq_tail = inst->cq_head;

        NVSNAP_INFO("  Synced indices: sq=%u/%u cq=%u/%u", *sq_head, *sq_tail, *cq_head, *cq_tail);

        /* Set NEED_WAKEUP since we stripped SQPOLL */
        *sqflags |= IORING_SQ_NEED_WAKEUP;
        NVSNAP_INFO("  Set IORING_SQ_NEED_WAKEUP in sqflags");
    }

    /* Move new fd to expected fd number */
    if (new_fd != inst->fd) {
        if (!real_close) {
            real_close = dlsym(RTLD_NEXT, "close");
        }

        /* Close whatever is at the target fd */
        real_close(inst->fd);

        if (dup2(new_fd, inst->fd) < 0) {
            NVSNAP_WARN("  dup2(%d, %d) failed: %s", new_fd, inst->fd, strerror(errno));
            real_close(new_fd);
            return -1;
        }
        real_close(new_fd);
        NVSNAP_INFO("  Moved io_uring from fd %d to fd %d", new_fd, inst->fd);
    }

    /* Update tracking with new params */
    inst->params = new_params;
    inst->reinit_succeeded = 1;
    inst->reinit_attempts++;
    inst->validated_after_restore = 1;

    NVSNAP_WARN("=== TRANSPARENT REINIT COMPLETE for fd=%d ===", inst->fd);
    return 0;
}

/*
 * =============================================================================
 * PROACTIVE REINIT
 * =============================================================================
 */

static _Atomic int g_proactive_reinit_done = 0;
static _Atomic int g_pre_restore_check_done = 0;

/*
 * Force reinit mode - set to 1 when we detect EBADF (indicating restore)
 */
static _Atomic int g_force_reinit_mode = 0;

static void proactive_reinit_all_io_urings(void) {
    if (g_proactive_reinit_done) return;
    g_proactive_reinit_done = 1;

    if (g_force_reinit_mode) {
        NVSNAP_WARN("=== PROACTIVE IO_URING REINIT (FORCED - post-restore) ===");
    } else {
        NVSNAP_INFO("=== PROACTIVE IO_URING REINIT (checking validity) ===");
    }

    pthread_mutex_lock(&g_io_uring_mutex);

    int reinit_count = 0;
    int success_count = 0;
    int skipped_valid = 0;

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (!g_io_urings[i].active) continue;

        nvsnap_io_uring_instance_t *inst = &g_io_urings[i];

        if (inst->validated_after_restore || inst->reinit_succeeded || inst->reinit_attempts >= 3) continue;

        NVSNAP_INFO("Checking io_uring fd=%d (entries=%u, flags=0x%x, SQPOLL=%d)",
                   inst->fd, inst->setup_entries, inst->params.flags,
                   (inst->params.flags & IORING_SETUP_SQPOLL) != 0);

        if (!g_force_reinit_mode && is_valid_io_uring_fd(inst->fd)) {
            inst->validated_after_restore = 1;
            NVSNAP_INFO("  fd=%d is valid io_uring - skipping reinit", inst->fd);
            skipped_valid++;
            continue;
        }

        char fd_type[256] = "unknown";
        get_fd_type(inst->fd, fd_type, sizeof(fd_type));
        NVSNAP_WARN("  fd=%d (now: %s) - reinitializing", inst->fd, fd_type);

        reinit_count++;

        if (inst->sq_ring_addr == 0 || inst->sqe_addr == 0) {
            NVSNAP_WARN("  Cannot reinit fd=%d - missing mmap addresses (sq=0x%lx sqe=0x%lx)",
                       inst->fd, inst->sq_ring_addr, inst->sqe_addr);
            inst->reinit_attempts = 3;
            continue;
        }

        if (reinit_io_uring_at_fd(inst) == 0) {
            success_count++;
            NVSNAP_INFO("  fd=%d reinit SUCCESS", inst->fd);
        } else {
            NVSNAP_WARN("  fd=%d reinit FAILED", inst->fd);
        }
    }

    pthread_mutex_unlock(&g_io_uring_mutex);

    NVSNAP_WARN("=== PROACTIVE REINIT COMPLETE: %d/%d reinited, %d skipped (valid) ===",
               success_count, reinit_count, skipped_valid);

    if (success_count > 0 || g_force_reinit_mode) {
        /* io_uring reinit complete. uvloop handles uv_loop_fork() natively now. */
        NVSNAP_INFO("Manual io_uring reinit complete (uvloop handles uv_loop_fork natively)");
    }
}

/*
 * =============================================================================
 * TRACKING FUNCTIONS (exported to quiesce.c)
 * =============================================================================
 */

static void nvsnap_write_io_uring_map_locked(void) {
    FILE *f = fopen("/nvsnap-lib/.io_uring_map", "w");
    if (!f) {
        NVSNAP_WARN("Failed to write /nvsnap-lib/.io_uring_map: %s", strerror(errno));
        return;
    }

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (!g_io_urings[i].active)
            continue;
        fprintf(f, "%d %u %u 0x%x\n",
                g_io_urings[i].fd,
                g_io_urings[i].params.sq_entries,
                g_io_urings[i].params.cq_entries,
                g_io_urings[i].params.flags);
    }
    fflush(f);
    fsync(fileno(f));
    fclose(f);
}

/* Track a new io_uring instance */
int nvsnap_track_io_uring(int fd, uint32_t sq_entries, uint32_t cq_entries,
                          uint32_t flags) {
    pthread_mutex_lock(&g_io_uring_mutex);

    if (g_io_uring_count >= MAX_IO_URING_INSTANCES) {
        NVSNAP_WARN("Too many io_uring instances (%d), can't track fd=%d",
                   g_io_uring_count, fd);
        pthread_mutex_unlock(&g_io_uring_mutex);
        return -1;
    }

    /* Find empty slot */
    int slot = -1;
    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (!g_io_urings[i].active) {
            slot = i;
            break;
        }
    }

    if (slot < 0) {
        NVSNAP_WARN("No empty io_uring slots");
        pthread_mutex_unlock(&g_io_uring_mutex);
        return -1;
    }

    memset(&g_io_urings[slot], 0, sizeof(nvsnap_io_uring_instance_t));
    g_io_urings[slot].fd = fd;
    g_io_urings[slot].active = 1;
    g_io_urings[slot].setup_entries = sq_entries;
    g_io_urings[slot].params.sq_entries = sq_entries;
    g_io_urings[slot].params.cq_entries = cq_entries;
    g_io_urings[slot].params.flags = flags;
    g_io_urings[slot].owner_thread = pthread_self();
    g_io_uring_count++;

    NVSNAP_INFO("Tracked io_uring fd=%d entries=%u/%u flags=0x%x (SQPOLL=%d)",
               fd, sq_entries, cq_entries, flags, (flags & IORING_SETUP_SQPOLL) != 0);

    nvsnap_write_io_uring_map_locked();

    pthread_mutex_unlock(&g_io_uring_mutex);
    return 0;
}

/* Update mmap addresses for a tracked io_uring (call after mmap is done) */
int nvsnap_update_io_uring_addrs(int fd) {
    pthread_mutex_lock(&g_io_uring_mutex);

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (g_io_urings[i].active && g_io_urings[i].fd == fd) {
            capture_io_uring_mmap_addrs(fd, &g_io_urings[i]);

            /* Also capture current ring indices */
            parse_io_uring_fdinfo(fd,
                                   &g_io_urings[i].params.sq_entries,
                                   &g_io_urings[i].params.cq_entries,
                                   &g_io_urings[i].sq_head,
                                   &g_io_urings[i].sq_tail,
                                   &g_io_urings[i].cq_head,
                                   &g_io_urings[i].cq_tail);

            NVSNAP_DEBUG("Updated io_uring fd=%d addrs: sq=0x%lx sqe=0x%lx indices: sq=%u/%u cq=%u/%u",
                        fd, g_io_urings[i].sq_ring_addr, g_io_urings[i].sqe_addr,
                        g_io_urings[i].sq_head, g_io_urings[i].sq_tail,
                        g_io_urings[i].cq_head, g_io_urings[i].cq_tail);

            pthread_mutex_unlock(&g_io_uring_mutex);
            return 0;
        }
    }

    pthread_mutex_unlock(&g_io_uring_mutex);
    return -1;
}

/* Untrack an io_uring instance (on close) */
int nvsnap_untrack_io_uring(int fd) {
    pthread_mutex_lock(&g_io_uring_mutex);

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (g_io_urings[i].active && g_io_urings[i].fd == fd) {
            NVSNAP_DEBUG("Untracked io_uring fd=%d", fd);
            g_io_urings[i].active = 0;
            g_io_uring_count--;
            pthread_mutex_unlock(&g_io_uring_mutex);
            return 0;
        }
    }

    pthread_mutex_unlock(&g_io_uring_mutex);
    return -1;
}

/* Check if an io_uring fd needs reinit after restore */
int nvsnap_io_uring_needs_reinit(int fd) {
    pthread_mutex_lock(&g_io_uring_mutex);

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (g_io_urings[i].active && g_io_urings[i].fd == fd) {
            int needs = !g_io_urings[i].validated_after_restore &&
                        !g_io_urings[i].reinit_succeeded &&
                        g_io_urings[i].reinit_attempts < 3;
            pthread_mutex_unlock(&g_io_uring_mutex);
            return needs;
        }
    }

    pthread_mutex_unlock(&g_io_uring_mutex);
    return 0;
}

/* Mark an io_uring fd as validated after restore */
void nvsnap_mark_io_uring_validated(int fd) {
    pthread_mutex_lock(&g_io_uring_mutex);

    for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
        if (g_io_urings[i].active && g_io_urings[i].fd == fd) {
            g_io_urings[i].validated_after_restore = 1;
            NVSNAP_DEBUG("io_uring fd=%d marked as validated after restore", fd);
            break;
        }
    }

    pthread_mutex_unlock(&g_io_uring_mutex);
}

/*
 * =============================================================================
 * SYSCALL INTERCEPTION
 * =============================================================================
 */

long syscall(long number, ...) {
    init_real_syscall();

    /* Check for quiesce request on any syscall (opportunistic) */
    nvsnap_perform_quiescence();

    va_list ap;
    va_start(ap, number);

    long ret;

    switch (number) {
        case __NR_io_uring_setup: {
            /* io_uring_setup(entries, params) */
            unsigned int entries = va_arg(ap, unsigned int);
            struct io_uring_params* params = va_arg(ap, struct io_uring_params*);
            va_end(ap);

            NVSNAP_DEBUG("io_uring_setup(entries=%u, params=%p)", entries, params);

            ret = real_syscall(__NR_io_uring_setup, entries, params);

            if (ret >= 0) {
                uint32_t sq_entries = params ? params->sq_entries : entries;
                uint32_t cq_entries = params ? params->cq_entries : entries * 2;
                uint32_t flags = params ? params->flags : 0;

                nvsnap_track_io_uring((int)ret, sq_entries, cq_entries, flags);

                /* Store full params */
                pthread_mutex_lock(&g_io_uring_mutex);
                for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
                    if (g_io_urings[i].active && g_io_urings[i].fd == (int)ret) {
                        g_io_urings[i].setup_entries = entries;
                        if (params) {
                            g_io_urings[i].params = *params;
                        }
                        break;
                    }
                }
                pthread_mutex_unlock(&g_io_uring_mutex);

                if (flags & IORING_SETUP_SQPOLL) {
                    NVSNAP_WARN("io_uring fd=%ld has SQPOLL - kernel thread created", ret);
                }
            }

            return ret;
        }

        case __NR_io_uring_enter: {
            /* io_uring_enter(fd, to_submit, min_complete, flags, sig) */
            unsigned int fd = va_arg(ap, unsigned int);
            unsigned int to_submit = va_arg(ap, unsigned int);
            unsigned int min_complete = va_arg(ap, unsigned int);
            unsigned int flags = va_arg(ap, unsigned int);
            void* sig = va_arg(ap, void*);
            va_end(ap);

            NVSNAP_TRACE("io_uring_enter(fd=%u, submit=%u, complete=%u, flags=0x%x)",
                        fd, to_submit, min_complete, flags);
            if (is_debug_io_uring_enabled()) {
                NVSNAP_INFO("io_uring_enter intercepted: fd=%u submit=%u complete=%u flags=0x%x",
                           fd, to_submit, min_complete, flags);
            }

            if (is_io_uring_reinit_disabled()) {
                ret = real_syscall(__NR_io_uring_enter, fd, to_submit, min_complete,
                                   flags, sig);
                if (is_debug_io_uring_enabled()) {
                    NVSNAP_INFO("io_uring_enter(fd=%u) -> ret=%ld errno=%d (%s)",
                               fd, ret, errno, strerror(errno));
                }
                return ret;
            }

            /* Pre-restore check: detect restore via marker files */
            if (!g_pre_restore_check_done && g_io_uring_count > 0) {
                int is_restored = 0;
                if (access(CRIU_RESTORE_MARKER, F_OK) == 0) {
                    NVSNAP_WARN("Restore marker found: %s", CRIU_RESTORE_MARKER);
                    is_restored = 1;
                } else if (access(NVSNAP_RESTORE_MARKER, F_OK) == 0) {
                    NVSNAP_WARN("Restore marker found: %s", NVSNAP_RESTORE_MARKER);
                    is_restored = 1;
                } else if (access("/nvsnap-lib/.restored", F_OK) == 0) {
                    NVSNAP_WARN("Restore marker found: /nvsnap-lib/.restored");
                    is_restored = 1;
                } else {
                    /* Also check by detecting broken io_uring */
                    char fd_type[256];
                    if (get_fd_type(fd, fd_type, sizeof(fd_type)) == 0) {
                        if (strstr(fd_type, "io_uring") == NULL) {
                            NVSNAP_WARN("io_uring fd=%u is now: %s - RESTORE DETECTED!", fd, fd_type);
                            is_restored = 1;
                        }
                    }
                }

                if (is_restored) {
                    NVSNAP_WARN("=== RESTORE DETECTED (pre-syscall) ===");
                    /* uvloop handles uv_loop_fork natively now - just do io_uring reinit */
                    g_pre_restore_check_done = 1;
                }
                g_pre_restore_check_done = 1;
            }

            /* Proactive reinit on first io_uring_enter after restore */
            if (!g_proactive_reinit_done && g_io_uring_count > 0) {
                char fd_type[256];
                int fd_is_invalid = 0;
                if (get_fd_type(fd, fd_type, sizeof(fd_type)) == 0) {
                    if (strstr(fd_type, "io_uring") == NULL) {
                        NVSNAP_WARN("io_uring fd=%u is now: %s - RESTORE DETECTED!", fd, fd_type);
                        fd_is_invalid = 1;
                    }
                }

                if (fd_is_invalid) {
                    g_proactive_reinit_done = 1;
                    /* uvloop handles uv_loop_fork natively - just reinit io_urings */
                }

                proactive_reinit_all_io_urings();
            }

            /* Execute the actual syscall */
            ret = real_syscall(__NR_io_uring_enter, fd, to_submit, min_complete,
                              flags, sig);
            if (is_debug_io_uring_enabled()) {
                NVSNAP_INFO("io_uring_enter(fd=%u) -> ret=%ld errno=%d (%s)",
                           fd, ret, errno, strerror(errno));
            }

            if (is_debug_io_uring_enabled() && (flags & IORING_ENTER_GETEVENTS)) {
                if (ret == -1 && errno == EINTR) {
                    NVSNAP_WARN("io_uring_enter(fd=%u) EINTR: submit=%u min=%u flags=0x%x",
                               fd, to_submit, min_complete, flags);
                }

                if (ret >= 0 && min_complete > 0 && ret != (long)min_complete) {
                    NVSNAP_WARN("io_uring_enter(fd=%u) short getevents: ret=%ld expected=%u submit=%u flags=0x%x",
                               fd, ret, min_complete, to_submit, flags);
                    dump_io_uring_fdinfo(fd, "short-getevents");
                    void *bt[32];
                    int bt_size = backtrace(bt, (int)(sizeof(bt) / sizeof(bt[0])));
                    backtrace_symbols_fd(bt, bt_size, STDERR_FILENO);
                }
            }

            /* If EBADF or ENOTSUP, try reinit and retry */
            if (ret < 0 && (errno == EBADF || errno == ENOTSUP || errno == EOPNOTSUPP)) {
                int saved_errno = errno;

                NVSNAP_WARN("io_uring_enter(fd=%u) got %s - this indicates CRIU restore!",
                           fd, strerror(errno));

                char fd_type2[256];
                if (get_fd_type(fd, fd_type2, sizeof(fd_type2)) == 0) {
                    NVSNAP_WARN("  fd=%u is currently: %s", fd, fd_type2);
                }

                /* Reset and force reinit all io_urings */
                NVSNAP_WARN("Resetting proactive reinit flag and enabling FORCE mode");
                g_proactive_reinit_done = 0;
                g_force_reinit_mode = 1;

                /* Reset validation state for ALL tracked io_urings */
                pthread_mutex_lock(&g_io_uring_mutex);
                for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
                    if (g_io_urings[i].active) {
                        g_io_urings[i].validated_after_restore = 0;
                        g_io_urings[i].reinit_succeeded = 0;
                        g_io_urings[i].reinit_attempts = 0;
                    }
                }
                pthread_mutex_unlock(&g_io_uring_mutex);

                NVSNAP_WARN("Running proactive reinit on ALL tracked io_urings");
                proactive_reinit_all_io_urings();

                nvsnap_perform_restore_reinit();

                /* Retry the syscall */
                ret = real_syscall(__NR_io_uring_enter, fd, to_submit, min_complete,
                                   flags, sig);

                if (ret >= 0) {
                    NVSNAP_INFO("io_uring_enter(fd=%u) SUCCEEDED after proactive reinit!", fd);
                } else {
                    NVSNAP_WARN("io_uring_enter(fd=%u) still failed after reinit: %s",
                               fd, strerror(errno));
                    errno = saved_errno;
                }
            }

            return ret;
        }

        case __NR_io_uring_register: {
            unsigned int fd = va_arg(ap, unsigned int);
            unsigned int opcode = va_arg(ap, unsigned int);
            void* arg = va_arg(ap, void*);
            unsigned int nr_args = va_arg(ap, unsigned int);
            va_end(ap);

            NVSNAP_DEBUG("io_uring_register(fd=%u, opcode=%u, arg=%p, nr=%u)",
                        fd, opcode, arg, nr_args);

            ret = real_syscall(__NR_io_uring_register, fd, opcode, arg, nr_args);
            return ret;
        }

        case __NR_close: {
            int fd = va_arg(ap, int);
            va_end(ap);

            if (check_restored() && nvsnap_io_uring_needs_reinit(fd)) {
                NVSNAP_WARN("CLOSE (via syscall) called on io_uring fd=%d AFTER RESTORE!", fd);
            }

            nvsnap_untrack_io_uring(fd);
            ret = real_syscall(__NR_close, fd);
            return ret;
        }

        default: {
            long a1 = va_arg(ap, long);
            long a2 = va_arg(ap, long);
            long a3 = va_arg(ap, long);
            long a4 = va_arg(ap, long);
            long a5 = va_arg(ap, long);
            long a6 = va_arg(ap, long);
            va_end(ap);

            return real_syscall(number, a1, a2, a3, a4, a5, a6);
        }
    }
}

/*
 * =============================================================================
 * LIBURING INTERCEPTION (higher-level library)
 * =============================================================================
 */

typedef int (*io_uring_queue_init_fn)(unsigned entries, void* ring, unsigned flags);
static io_uring_queue_init_fn real_io_uring_queue_init = NULL;

int io_uring_queue_init(unsigned entries, void* ring, unsigned flags) {
    if (!real_io_uring_queue_init) {
        real_io_uring_queue_init = dlsym(RTLD_NEXT, "io_uring_queue_init");
        if (!real_io_uring_queue_init) {
            NVSNAP_WARN("io_uring_queue_init not found - liburing not loaded?");
            errno = ENOSYS;
            return -1;
        }
    }

    NVSNAP_DEBUG("io_uring_queue_init(entries=%u, ring=%p, flags=0x%x)",
                entries, ring, flags);

    int ret = real_io_uring_queue_init(entries, ring, flags);

    if (ret == 0) {
        int ring_fd = *(int*)ring;
        nvsnap_track_io_uring(ring_fd, entries, entries * 2, flags);
        nvsnap_update_io_uring_addrs(ring_fd);

        NVSNAP_INFO("io_uring_queue_init succeeded: fd=%d entries=%u flags=0x%x",
                   ring_fd, entries, flags);
    }

    return ret;
}

typedef int (*io_uring_queue_init_params_fn)(unsigned entries, void* ring,
                                              struct io_uring_params* p);
static io_uring_queue_init_params_fn real_io_uring_queue_init_params = NULL;

int io_uring_queue_init_params(unsigned entries, void* ring, struct io_uring_params* p) {
    if (!real_io_uring_queue_init_params) {
        real_io_uring_queue_init_params = dlsym(RTLD_NEXT, "io_uring_queue_init_params");
        if (!real_io_uring_queue_init_params) {
            NVSNAP_WARN("io_uring_queue_init_params not found");
            errno = ENOSYS;
            return -1;
        }
    }

    NVSNAP_DEBUG("io_uring_queue_init_params(entries=%u, ring=%p, params=%p)",
                entries, ring, p);

    int ret = real_io_uring_queue_init_params(entries, ring, p);

    if (ret == 0) {
        int ring_fd = *(int*)ring;
        uint32_t sq_entries = p ? p->sq_entries : entries;
        uint32_t cq_entries = p ? p->cq_entries : entries * 2;
        uint32_t flags = p ? p->flags : 0;

        nvsnap_track_io_uring(ring_fd, sq_entries, cq_entries, flags);

        pthread_mutex_lock(&g_io_uring_mutex);
        for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
            if (g_io_urings[i].active && g_io_urings[i].fd == ring_fd && p) {
                g_io_urings[i].params = *p;
                break;
            }
        }
        pthread_mutex_unlock(&g_io_uring_mutex);

        nvsnap_update_io_uring_addrs(ring_fd);

        NVSNAP_INFO("io_uring_queue_init_params succeeded: fd=%d sq=%u cq=%u flags=0x%x",
                   ring_fd, sq_entries, cq_entries, flags);
    }

    return ret;
}

typedef void (*io_uring_queue_exit_fn)(void* ring);
static io_uring_queue_exit_fn real_io_uring_queue_exit = NULL;

void io_uring_queue_exit(void* ring) {
    if (!real_io_uring_queue_exit) {
        real_io_uring_queue_exit = dlsym(RTLD_NEXT, "io_uring_queue_exit");
        if (!real_io_uring_queue_exit) {
            return;
        }
    }

    if (ring) {
        int ring_fd = *(int*)ring;
        NVSNAP_DEBUG("io_uring_queue_exit(ring=%p fd=%d)", ring, ring_fd);
        nvsnap_untrack_io_uring(ring_fd);
    }

    real_io_uring_queue_exit(ring);
}

/*
 * =============================================================================
 * DIRECT close() INTERCEPTION
 * =============================================================================
 */

int close(int fd) {
    if (!real_close) {
        real_close = dlsym(RTLD_NEXT, "close");
        if (!real_close) {
            errno = EBADF;
            return -1;
        }
    }

    if (check_restored() && nvsnap_io_uring_needs_reinit(fd)) {
        NVSNAP_WARN("close(%d) called on io_uring fd AFTER RESTORE", fd);
    }

    nvsnap_untrack_io_uring(fd);
    return real_close(fd);
}

/*
 * =============================================================================
 * MMAP INTERCEPTION - Capture io_uring ring addresses
 * =============================================================================
 */

void *mmap(void *addr, size_t length, int prot, int flags, int fd, off_t offset) {
    if (!real_mmap) {
        real_mmap = dlsym(RTLD_NEXT, "mmap");
        if (!real_mmap) {
            errno = ENOMEM;
            return MAP_FAILED;
        }
    }

    void *result = real_mmap(addr, length, prot, flags, fd, offset);

    /* If this is an io_uring mmap, capture the address */
    if (result != MAP_FAILED && fd >= 0) {
        pthread_mutex_lock(&g_io_uring_mutex);
        for (int i = 0; i < MAX_IO_URING_INSTANCES; i++) {
            if (g_io_urings[i].active && g_io_urings[i].fd == fd) {
                if (offset == IORING_OFF_SQ_RING || offset == 0) {
                    g_io_urings[i].sq_ring_addr = (unsigned long)result;
                    g_io_urings[i].sq_ring_size = length;
                    NVSNAP_DEBUG("Captured io_uring fd=%d SQ ring mmap: addr=%p size=%zu",
                                fd, result, length);
                } else if (offset == IORING_OFF_CQ_RING) {
                    g_io_urings[i].cq_ring_addr = (unsigned long)result;
                    g_io_urings[i].cq_ring_size = length;
                    NVSNAP_DEBUG("Captured io_uring fd=%d CQ ring mmap: addr=%p size=%zu",
                                fd, result, length);
                } else if (offset == IORING_OFF_SQES) {
                    g_io_urings[i].sqe_addr = (unsigned long)result;
                    g_io_urings[i].sqe_size = length;
                    NVSNAP_DEBUG("Captured io_uring fd=%d SQEs mmap: addr=%p size=%zu",
                                fd, result, length);
                }
                break;
            }
        }
        pthread_mutex_unlock(&g_io_uring_mutex);
    }

    return result;
}

/* Also intercept mmap64 which some libraries use */
void *mmap64(void *addr, size_t length, int prot, int flags, int fd, off_t offset) {
    return mmap(addr, length, prot, flags, fd, offset);
}

/* Stubs for removed uvloop functions (still referenced from header) */
void nvsnap_dump_uvloop_metadata(void) {
    /* No-op: uvloop handles uv_loop_fork natively now */
}

void nvsnap_install_uvloop_hook_async(void) {
    /* No-op: uvloop handles uv_loop_fork natively now */
}

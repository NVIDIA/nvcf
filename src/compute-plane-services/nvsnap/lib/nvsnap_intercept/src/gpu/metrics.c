/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Shared-memory ring buffer for zero-syscall metrics.
 *
 * Uses MAP_ANONYMOUS instead of shm_open so CRIU can checkpoint/restore
 * the mapping natively without needing /dev/shm/ files to exist.
 */
#include "nvsnap/gpu/metrics.h"
#include "nvsnap/gpu/config.h"

#include <stdatomic.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

static NvSnapMetricsRingBuffer *g_ring = NULL;

static uint64_t metrics_now_ns(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
}

int nvsnap_metrics_init(void)
{
    if (g_ring)
        return 0; /* already initialized */

    /* Anonymous mmap — no /dev/shm file, no path for CRIU to track.
     * CRIU handles MAP_ANONYMOUS | MAP_SHARED natively. */
    g_ring = (NvSnapMetricsRingBuffer *)mmap(
        NULL, sizeof(NvSnapMetricsRingBuffer),
        PROT_READ | PROT_WRITE,
        MAP_ANONYMOUS | MAP_SHARED, -1, 0);

    if (g_ring == MAP_FAILED) {
        g_ring = NULL;
        return -1;
    }

    memset(g_ring, 0, sizeof(NvSnapMetricsRingBuffer));
    return 0;
}

void nvsnap_metrics_write(NvSnapMetricType type, int device,
                            uint64_t value, uint64_t ptr)
{
    NvSnapMetricsRingBuffer *ring = g_ring;
    if (__builtin_expect(ring == NULL, 0))
        return;

    /* Atomically claim a slot. */
    uint64_t pos = atomic_fetch_add_explicit(&ring->write_pos, 1,
                                              memory_order_relaxed);
    uint64_t idx = pos & (NVSNAP_METRICS_RING_SIZE - 1);

    /* Check for overflow (writer lapping reader). */
    uint64_t rpos = atomic_load_explicit(&ring->read_pos, memory_order_relaxed);
    if (pos - rpos >= NVSNAP_METRICS_RING_SIZE) {
        atomic_fetch_add_explicit(&ring->overflow_count, 1,
                                   memory_order_relaxed);
    }

    NvSnapMetricsEntry *e = &ring->entries[idx];
    e->type      = (uint32_t)type;
    e->pid       = (uint32_t)getpid();
    e->device    = (int32_t)device;
    e->timestamp = metrics_now_ns();
    e->value     = value;
    e->ptr       = ptr;
}

int nvsnap_metrics_read(NvSnapMetricsEntry *out)
{
    NvSnapMetricsRingBuffer *ring = g_ring;
    if (__builtin_expect(ring == NULL, 0))
        return -1;

    uint64_t rpos = atomic_load_explicit(&ring->read_pos, memory_order_relaxed);
    uint64_t wpos = atomic_load_explicit(&ring->write_pos, memory_order_acquire);

    if (rpos >= wpos)
        return -1; /* empty */

    uint64_t idx = rpos & (NVSNAP_METRICS_RING_SIZE - 1);
    if (out)
        *out = ring->entries[idx];

    atomic_fetch_add_explicit(&ring->read_pos, 1, memory_order_release);
    return 0;
}

NvSnapMetricsRingBuffer *nvsnap_metrics_get_buffer(void)
{
    return g_ring;
}

void nvsnap_metrics_destroy(void)
{
    if (g_ring) {
        munmap(g_ring, sizeof(NvSnapMetricsRingBuffer));
        g_ring = NULL;
    }
}

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Shared-memory ring buffer for zero-syscall metrics.
 */
#ifndef NVSNAP_METRICS_H
#define NVSNAP_METRICS_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
#define _Atomic volatile
extern "C" {
#else
#include <stdatomic.h>
#endif

/* ── Metric types ────────────────────────────────────────────────────── */

typedef enum {
    NVSNAP_METRIC_ALLOC = 0,
    NVSNAP_METRIC_FREE,
    NVSNAP_METRIC_KERNEL_LAUNCH,
    NVSNAP_METRIC_MEMCPY,
    NVSNAP_METRIC_NCCL_COLLECTIVE,
    NVSNAP_METRIC_STREAM_CREATE,
    NVSNAP_METRIC_STREAM_DESTROY,
    NVSNAP_METRIC_EVENT_CREATE,
    NVSNAP_METRIC_EVENT_DESTROY,
    NVSNAP_METRIC_DEVICE_SYNC,
    NVSNAP_METRIC_VMM_MAP,
    NVSNAP_METRIC_VMM_UNMAP,
    NVSNAP_METRIC_TYPE_COUNT
} NvSnapMetricType;

/* ── Metrics entry — cache-line aligned (64 bytes) ───────────────────── */

typedef struct {
    uint32_t type;      /* NvSnapMetricType */
    uint32_t pid;
    int32_t  device;
    uint32_t _pad0;
    uint64_t timestamp; /* nanoseconds since epoch */
    uint64_t value;     /* size in bytes, latency, count, etc. */
    uint64_t ptr;       /* relevant pointer / handle */
    uint8_t  _pad1[24]; /* pad to 64 bytes total */
} __attribute__((aligned(64))) NvSnapMetricsEntry;

/* ── Ring buffer ─────────────────────────────────────────────────────── */

#define NVSNAP_METRICS_RING_SIZE 8192 /* must be power of 2 */

typedef struct {
    _Atomic uint64_t write_pos;
    uint8_t          _pad_w[56]; /* separate cache lines */
    _Atomic uint64_t read_pos;
    uint8_t          _pad_r[56];
    _Atomic uint32_t overflow_count;
    uint8_t          _pad_o[60];
    NvSnapMetricsEntry entries[NVSNAP_METRICS_RING_SIZE];
} NvSnapMetricsRingBuffer;

/* ── C API ───────────────────────────────────────────────────────────── */

int  nvsnap_metrics_init(void);
void nvsnap_metrics_write(NvSnapMetricType type, int device,
                            uint64_t value, uint64_t ptr);
int  nvsnap_metrics_read(NvSnapMetricsEntry *out);
void nvsnap_metrics_destroy(void);

/* Access the ring buffer directly (e.g., from an agent process). */
NvSnapMetricsRingBuffer *nvsnap_metrics_get_buffer(void);

#ifdef __cplusplus
}
#undef _Atomic
#endif

#endif /* NVSNAP_METRICS_H */

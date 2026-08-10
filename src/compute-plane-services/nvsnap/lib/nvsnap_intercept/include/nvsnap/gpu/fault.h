/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Fault injection.
 */
#ifndef NVSNAP_FAULT_H
#define NVSNAP_FAULT_H

#include <stdint.h>

#ifdef __cplusplus
/* C++ doesn't support C11 _Atomic. The struct fields are only
 * accessed atomically from fault.c (pure C). C++ code only sees
 * the struct layout for sizeof/offsetof compatibility. */
#define _Atomic volatile
extern "C" {
#else
#include <stdatomic.h>
#endif

typedef enum {
    NVSNAP_FAULT_OOM = 0,
    NVSNAP_FAULT_KERNEL_DROP,
    NVSNAP_FAULT_ECC_ERROR,
    NVSNAP_FAULT_MEMCPY_LATENCY,
    NVSNAP_FAULT_NCCL_FAIL,
    NVSNAP_FAULT_NCCL_TIMEOUT,
    NVSNAP_FAULT_TYPE_COUNT
} NvSnapFaultType;

typedef struct {
    NvSnapFaultType type;
    double            probability;   /* 0.0 .. 1.0 */
    uint64_t          after_count;   /* start injecting after N calls */
    _Atomic uint64_t  current_count;
    _Atomic int       enabled;
} NvSnapFaultRule;

void nvsnap_fault_init(void);
int  nvsnap_fault_check(NvSnapFaultType type); /* returns 1 if fault should fire */
void nvsnap_fault_add_rule(NvSnapFaultType type, double probability,
                             uint64_t after_count);
void nvsnap_fault_clear(void);

#ifdef __cplusplus
}
#undef _Atomic
#endif

#endif /* NVSNAP_FAULT_H */

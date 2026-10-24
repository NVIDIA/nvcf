/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap -- GPU checkpoint/restore API.
 *
 * Saves and restores GPU state (allocations, streams, events, metadata)
 * to/from a directory on disk. Foundation for live GPU migration.
 */
#ifndef NVSNAP_CHECKPOINT_H
#define NVSNAP_CHECKPOINT_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ── Checkpoint file format structures ───────────────────────────────── */

#define NVSNAP_CHECKPOINT_MAGIC   0x57524150 /* "WRAP" */
#define NVSNAP_CHECKPOINT_VERSION 1

typedef struct {
    uint32_t magic;           /* 0x57524150 ("WRAP") */
    uint32_t version;         /* 1 */
    uint32_t num_allocations;
    uint32_t num_streams;
    uint32_t num_events;
    uint32_t num_nccl_comms;
    uint32_t num_vmm_mappings;
    uint32_t source_device;   /* original GPU device ordinal */
    uint64_t total_gpu_bytes; /* total GPU memory saved */
    uint64_t timestamp;       /* checkpoint creation time (ns since epoch) */
} NvSnapCheckpointHeader;

typedef struct {
    uint64_t ptr;           /* original GPU virtual address */
    uint64_t size;          /* allocation size */
    int32_t  device;        /* device ordinal */
    uint32_t alloc_type;    /* AllocType enum value */
    uint64_t data_offset;   /* offset into gpu_data.bin where content is stored */
    uint32_t seq_num;       /* allocation sequence number (for replay ordering) */
    uint32_t pad;           /* maintain 8-byte alignment */
} NvSnapCheckpointAlloc;

typedef struct {
    uint64_t handle;        /* stream handle (opaque) */
    int32_t  device;
    uint32_t flags;
} NvSnapCheckpointStream;

typedef struct {
    uint64_t handle;        /* event handle (opaque) */
    int32_t  device;
    uint32_t flags;
} NvSnapCheckpointEvent;

typedef struct {
    uint64_t comm;          /* ncclComm_t handle (opaque) */
    int32_t  nranks;
    int32_t  rank;
    uint8_t  unique_id[128];
    int32_t  device;
    uint32_t _pad;
} NvSnapCheckpointNcclComm;

/* ── API ─────────────────────────────────────────────────────────────── */

/*
 * Checkpoint GPU state to a directory.
 *  1. Quiesces all GPU operations (cudaDeviceSynchronize)
 *  2. Saves all tracked GPU allocations (D2H copy)
 *  3. Saves metadata (allocation map, streams, events, NCCL comms)
 *
 * Files created:
 *   <checkpoint_dir>/gpu-<pid>/meta.bin    -- header + allocation/stream/event records
 *   <checkpoint_dir>/gpu-<pid>/gpu_data.bin -- raw GPU memory contents
 *
 * Returns 0 on success, -1 on error.
 */
int nvsnap_checkpoint_save(const char *checkpoint_dir);

/*
 * Restore GPU state from a checkpoint directory.
 *  1. Initializes CUDA driver (cuInit) and acquires fresh GPU context
 *  2. Reads metadata from <checkpoint_dir>/gpu-<pid>/meta.bin
 *  3. Reserves original GPU virtual addresses (cuMemAddressReserve)
 *     - Falls back to any VA if original is unavailable
 *  4. Creates physical allocations (cuMemCreate)
 *  5. Maps physical to VA (cuMemMap + cuMemSetAccess)
 *  6. Copies data from host to GPU (H2D)
 *
 * Returns 0 on success (all VAs preserved), 1 if some VAs were not preserved,
 * -1 on error.
 */
int nvsnap_checkpoint_restore(const char *checkpoint_dir);

/*
 * Restore GPU state for the CURRENT process only.
 * Looks for <checkpoint_dir>/gpu-<getpid()>/meta.bin.
 *
 * Use this when called from INSIDE a restored process (e.g., from
 * libnvsnap_intercept.so after CRIU restore). Each TP worker restores
 * its own GPU — CRIU preserves the original PID so gpu-<pid>/ matches.
 *
 * Returns 0 on success, 1 if VA not preserved, -1 on error.
 */
int nvsnap_checkpoint_restore_self(const char *checkpoint_dir);

/*
 * Pre-checkpoint quiesce: prepare GPU state for checkpoint.
 *
 * 1. Destroys all tracked NCCL communicators
 * 2. Disables P2P access between all GPU pairs
 * 3. Synchronizes all GPU devices
 *
 * Call this BEFORE cuCheckpointProcessLock/Checkpoint.
 * Without this, multi-GPU checkpoint hangs on NVLink P2P state.
 *
 * Returns 0 on success, -1 on error.
 */
int nvsnap_pre_checkpoint_quiesce(void);

/*
 * Post-restore resume: re-enable GPU state after restore.
 *
 * 1. Re-enables P2P access between all GPU pairs
 *
 * Call this AFTER cuCheckpointProcessRestore/Unlock.
 *
 * Returns 0 on success, -1 on error.
 */
int nvsnap_post_restore_resume(void);

/*
 * Query tracked state (for logging/diagnostics).
 */
int nvsnap_get_alloc_count(void);
uint64_t nvsnap_get_total_bytes(void);

#ifdef __cplusplus
}
#endif

#endif /* NVSNAP_CHECKPOINT_H */

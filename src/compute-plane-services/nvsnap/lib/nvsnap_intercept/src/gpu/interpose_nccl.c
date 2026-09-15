/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Minimal NCCL interception for checkpoint/restore.
 *
 * Only communicator lifecycle tracking is needed. Collective operations
 * are not intercepted — NvSnap handles NCCL quiesce separately.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <string.h>

#include "nvsnap/gpu/cuda_types.h"
#include "nvsnap/gpu/interpose.h"
#include "nvsnap/gpu/tracker.h"

/* ═══════════════════════════════════════════════════════════════════════
 * Communicator lifecycle — tracked for checkpoint/restore
 * ═══════════════════════════════════════════════════════════════════════ */

NVSNAP_DECLARE_REAL(ncclResult_t, ncclCommInitRank, ncclComm_t *, int, ncclUniqueId, int);

ncclResult_t ncclCommInitRank(ncclComm_t *comm, int nranks,
                              ncclUniqueId commId, int rank)
{
    NVSNAP_LOAD_REAL(ncclCommInitRank);

    ncclResult_t err = real_ncclCommInitRank(comm, nranks, commId, rank);

    if (err == ncclSuccess && comm && *comm) {
        nvsnap_tracker_track_nccl_comm(
            *comm, nranks, rank,
            (const unsigned char *)commId.internal,
            nvsnap_current_device);
        NVSNAP_GPU_LOG_INFO("ncclCommInitRank(nranks=%d, rank=%d) -> %p",
                          nranks, rank, *comm);
    }
    return err;
}

NVSNAP_DECLARE_REAL(ncclResult_t, ncclCommInitRankConfig, ncclComm_t *, int,
                      ncclUniqueId, int, void *);

ncclResult_t ncclCommInitRankConfig(ncclComm_t *comm, int nranks,
                                     ncclUniqueId commId, int rank,
                                     void *config)
{
    NVSNAP_LOAD_REAL(ncclCommInitRankConfig);

    ncclResult_t err = real_ncclCommInitRankConfig(comm, nranks, commId,
                                                    rank, config);

    if (err == ncclSuccess && comm && *comm) {
        nvsnap_tracker_track_nccl_comm(
            *comm, nranks, rank,
            (const unsigned char *)commId.internal,
            nvsnap_current_device);
        NVSNAP_GPU_LOG_INFO("ncclCommInitRankConfig(nranks=%d, rank=%d) -> %p",
                          nranks, rank, *comm);
    }
    return err;
}

NVSNAP_DECLARE_REAL(ncclResult_t, ncclCommInitAll, ncclComm_t *, int, const int *);

ncclResult_t ncclCommInitAll(ncclComm_t *comms, int ndev, const int *devlist)
{
    NVSNAP_LOAD_REAL(ncclCommInitAll);

    ncclResult_t err = real_ncclCommInitAll(comms, ndev, devlist);

    if (err == ncclSuccess && comms) {
        unsigned char zero_id[128] = {0};
        for (int i = 0; i < ndev; i++) {
            int dev = devlist ? devlist[i] : i;
            nvsnap_tracker_track_nccl_comm(comms[i], ndev, i, zero_id, dev);
        }
        NVSNAP_GPU_LOG_INFO("ncclCommInitAll(ndev=%d)", ndev);
    }
    return err;
}

NVSNAP_DECLARE_REAL(ncclResult_t, ncclCommDestroy, ncclComm_t);

ncclResult_t ncclCommDestroy(ncclComm_t comm)
{
    NVSNAP_LOAD_REAL(ncclCommDestroy);
    NVSNAP_GPU_LOG_INFO("ncclCommDestroy(%p)", comm);
    nvsnap_tracker_untrack_nccl_comm(comm);
    return real_ncclCommDestroy(comm);
}

NVSNAP_DECLARE_REAL(ncclResult_t, ncclCommAbort, ncclComm_t);

ncclResult_t ncclCommAbort(ncclComm_t comm)
{
    NVSNAP_LOAD_REAL(ncclCommAbort);
    NVSNAP_GPU_LOG_INFO("ncclCommAbort(%p)", comm);
    nvsnap_tracker_untrack_nccl_comm(comm);
    return real_ncclCommAbort(comm);
}

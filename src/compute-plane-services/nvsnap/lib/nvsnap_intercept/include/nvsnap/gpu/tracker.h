/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — GPU resource tracker (C++ header).
 */
#ifndef NVSNAP_TRACKER_H
#define NVSNAP_TRACKER_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus

#include <atomic>
#include <chrono>
#include <shared_mutex>
#include <string>
#include <unordered_map>
#include <vector>

namespace nvsnap {

enum class AllocType { DEVICE, MANAGED, HOST_PINNED, VMM };

struct AllocInfo {
    uintptr_t ptr;
    size_t    size;
    int       device;
    AllocType type;
    uint64_t  timestamp; /* nanoseconds since epoch */
    uint32_t  seq_num;   /* monotonic allocation sequence number */
};

struct StreamInfo {
    void    *handle;
    int      device;
    unsigned flags;
    uint64_t kernel_count;
};

struct EventInfo {
    void    *handle;
    int      device;
    unsigned flags;
};

struct NcclCommInfo {
    void    *comm;
    int      nranks;
    int      rank;
    uint8_t  unique_id[128];
    int      device;
    uint64_t collective_count;
    uint64_t bytes_transferred;
};

struct VmmMappingInfo {
    uintptr_t va;
    size_t    size;
    uint64_t  handle; /* CUmemGenericAllocationHandle */
    int       device;
};

struct GpuStats {
    uint64_t total_allocated_bytes;
    uint64_t total_kernel_launches;
    uint64_t total_memcpy_bytes;
    size_t   live_alloc_count;
    size_t   live_stream_count;
    size_t   live_event_count;
    size_t   live_nccl_comm_count;
    size_t   live_vmm_mapping_count;
};

class GpuTracker {
public:
    static GpuTracker &instance();

    /* ── Allocations ─────────────────────────────────────────────────── */
    void       track_alloc(uintptr_t ptr, size_t size, int device, AllocType type);
    bool       untrack_alloc(uintptr_t ptr);
    bool       lookup_alloc(uintptr_t ptr, AllocInfo *out) const;
    std::vector<AllocInfo> snapshot_allocs() const;

    /* ── Streams ─────────────────────────────────────────────────────── */
    void       track_stream(void *handle, int device, unsigned flags);
    bool       untrack_stream(void *handle);
    std::vector<StreamInfo> snapshot_streams() const;

    /* ── Events ──────────────────────────────────────────────────────── */
    void       track_event(void *handle, int device, unsigned flags);
    bool       untrack_event(void *handle);

    /* ── NCCL communicators ──────────────────────────────────────────── */
    void       track_nccl_comm(void *comm, int nranks, int rank,
                               const uint8_t unique_id[128], int device);
    bool       untrack_nccl_comm(void *comm);
    void       record_nccl_collective(void *comm, uint64_t bytes);
    std::vector<NcclCommInfo> snapshot_nccl_comms() const;

    /* ── VMM mappings ────────────────────────────────────────────────── */
    void       track_vmm_mapping(uintptr_t va, size_t size, uint64_t handle, int device);
    bool       untrack_vmm_mapping(uintptr_t va);
    std::vector<VmmMappingInfo> snapshot_vmm_mappings() const;

    /* ── Atomic counters (safe to call from hot path) ────────────────── */
    void       add_allocated_bytes(int64_t delta);
    void       inc_kernel_launches();
    void       add_memcpy_bytes(uint64_t bytes);
    void       inc_stream_kernel_count(void *stream);

    GpuStats   get_stats() const;

    /* ── Fork safety ─────────────────────────────────────────────────── */
    /* Reinitialize after fork(). Clears all tracked state and
     * reconstructs mutexes (which are in undefined state post-fork). */
    void       reset_after_fork();

private:
    GpuTracker() = default;
    GpuTracker(const GpuTracker &) = delete;
    GpuTracker &operator=(const GpuTracker &) = delete;

    mutable std::shared_mutex alloc_mu_;
    std::unordered_map<uintptr_t, AllocInfo> allocs_;

    mutable std::shared_mutex stream_mu_;
    std::unordered_map<uintptr_t, StreamInfo> streams_;

    mutable std::shared_mutex event_mu_;
    std::unordered_map<uintptr_t, EventInfo> events_;

    mutable std::shared_mutex nccl_mu_;
    std::unordered_map<uintptr_t, NcclCommInfo> nccl_comms_;

    mutable std::shared_mutex vmm_mu_;
    std::unordered_map<uintptr_t, VmmMappingInfo> vmm_mappings_;

    std::atomic<uint32_t> next_seq_num_{0};
    std::atomic<uint64_t> total_allocated_bytes_{0};
    std::atomic<uint64_t> total_kernel_launches_{0};
    std::atomic<uint64_t> total_memcpy_bytes_{0};
};

} /* namespace nvsnap */

/* ── C API for use from .c files ─────────────────────────────────────── */
extern "C" {
#endif /* __cplusplus */

void     nvsnap_tracker_track_alloc(uintptr_t ptr, size_t size, int device, int type);
void     nvsnap_tracker_untrack_alloc(uintptr_t ptr);
void     nvsnap_tracker_add_allocated_bytes(int64_t delta);
void     nvsnap_tracker_inc_kernel_launches(void);
void     nvsnap_tracker_add_memcpy_bytes(uint64_t bytes);

void     nvsnap_tracker_track_stream(void *handle, int device, unsigned flags);
void     nvsnap_tracker_untrack_stream(void *handle);
void     nvsnap_tracker_inc_stream_kernel_count(void *stream);

void     nvsnap_tracker_track_event(void *handle, int device, unsigned flags);
void     nvsnap_tracker_untrack_event(void *handle);

void     nvsnap_tracker_track_nccl_comm(void *comm, int nranks, int rank,
                                          const unsigned char unique_id[128], int device);
void     nvsnap_tracker_untrack_nccl_comm(void *comm);
void     nvsnap_tracker_record_nccl_collective(void *comm, uint64_t bytes);

void     nvsnap_tracker_track_vmm_mapping(uintptr_t va, size_t size, uint64_t handle, int device);
void     nvsnap_tracker_untrack_vmm_mapping(uintptr_t va);
void     nvsnap_tracker_reset_after_fork(void);

#ifdef __cplusplus
}
#endif

#endif /* NVSNAP_TRACKER_H */

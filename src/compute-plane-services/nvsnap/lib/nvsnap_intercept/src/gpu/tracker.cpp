/*
 * NvSnap — GpuTracker implementation.
 */
#include "nvsnap/gpu/tracker.h"

#include <chrono>
#include <cstring>
#include <mutex>

namespace nvsnap {

static uint64_t now_ns()
{
    using namespace std::chrono;
    return (uint64_t)duration_cast<nanoseconds>(
               steady_clock::now().time_since_epoch())
        .count();
}

GpuTracker &GpuTracker::instance()
{
    static GpuTracker s_instance;
    return s_instance;
}

/* ── Allocations ─────────────────────────────────────────────────────── */

void GpuTracker::track_alloc(uintptr_t ptr, size_t size, int device, AllocType type)
{
    AllocInfo info{ptr, size, device, type, now_ns(),
                   next_seq_num_.fetch_add(1, std::memory_order_relaxed)};
    std::unique_lock lock(alloc_mu_);
    allocs_[ptr] = info;
}

bool GpuTracker::untrack_alloc(uintptr_t ptr)
{
    std::unique_lock lock(alloc_mu_);
    auto it = allocs_.find(ptr);
    if (it == allocs_.end())
        return false;
    size_t size = it->second.size;
    allocs_.erase(it);
    /* Decrement live bytes (saturating). */
    uint64_t old = total_allocated_bytes_.load(std::memory_order_relaxed);
    while (old >= size &&
           !total_allocated_bytes_.compare_exchange_weak(
               old, old - size, std::memory_order_relaxed))
        ;
    return true;
}

bool GpuTracker::lookup_alloc(uintptr_t ptr, AllocInfo *out) const
{
    std::shared_lock lock(alloc_mu_);
    auto it = allocs_.find(ptr);
    if (it == allocs_.end())
        return false;
    if (out)
        *out = it->second;
    return true;
}

std::vector<AllocInfo> GpuTracker::snapshot_allocs() const
{
    std::shared_lock lock(alloc_mu_);
    std::vector<AllocInfo> v;
    v.reserve(allocs_.size());
    for (auto &kv : allocs_)
        v.push_back(kv.second);
    return v;
}

/* ── Streams ─────────────────────────────────────────────────────────── */

void GpuTracker::track_stream(void *handle, int device, unsigned flags)
{
    StreamInfo info{handle, device, flags, 0};
    std::unique_lock lock(stream_mu_);
    streams_[(uintptr_t)handle] = info;
}

bool GpuTracker::untrack_stream(void *handle)
{
    std::unique_lock lock(stream_mu_);
    return streams_.erase((uintptr_t)handle) > 0;
}

std::vector<StreamInfo> GpuTracker::snapshot_streams() const
{
    std::shared_lock lock(stream_mu_);
    std::vector<StreamInfo> v;
    v.reserve(streams_.size());
    for (auto &kv : streams_)
        v.push_back(kv.second);
    return v;
}

void GpuTracker::inc_stream_kernel_count(void *stream)
{
    if (!stream)
        return; /* default stream — skip map lookup on hot path */
    std::shared_lock lock(stream_mu_);
    auto it = streams_.find((uintptr_t)stream);
    if (it != streams_.end()) {
        /* Not perfectly atomic, but good enough for stats. */
        it->second.kernel_count++;
    }
}

/* ── Events ──────────────────────────────────────────────────────────── */

void GpuTracker::track_event(void *handle, int device, unsigned flags)
{
    EventInfo info{handle, device, flags};
    std::unique_lock lock(event_mu_);
    events_[(uintptr_t)handle] = info;
}

bool GpuTracker::untrack_event(void *handle)
{
    std::unique_lock lock(event_mu_);
    return events_.erase((uintptr_t)handle) > 0;
}

/* ── NCCL communicators ──────────────────────────────────────────────── */

void GpuTracker::track_nccl_comm(void *comm, int nranks, int rank,
                                 const uint8_t unique_id[128], int device)
{
    NcclCommInfo info{};
    info.comm = comm;
    info.nranks = nranks;
    info.rank = rank;
    info.device = device;
    info.collective_count = 0;
    info.bytes_transferred = 0;
    if (unique_id)
        std::memcpy(info.unique_id, unique_id, 128);
    std::unique_lock lock(nccl_mu_);
    nccl_comms_[(uintptr_t)comm] = info;
}

bool GpuTracker::untrack_nccl_comm(void *comm)
{
    std::unique_lock lock(nccl_mu_);
    return nccl_comms_.erase((uintptr_t)comm) > 0;
}

void GpuTracker::record_nccl_collective(void *comm, uint64_t bytes)
{
    std::shared_lock lock(nccl_mu_);
    auto it = nccl_comms_.find((uintptr_t)comm);
    if (it != nccl_comms_.end()) {
        it->second.collective_count++;
        it->second.bytes_transferred += bytes;
    }
}

std::vector<NcclCommInfo> GpuTracker::snapshot_nccl_comms() const
{
    std::shared_lock lock(nccl_mu_);
    std::vector<NcclCommInfo> v;
    v.reserve(nccl_comms_.size());
    for (auto &kv : nccl_comms_)
        v.push_back(kv.second);
    return v;
}

/* ── VMM mappings ────────────────────────────────────────────────────── */

void GpuTracker::track_vmm_mapping(uintptr_t va, size_t size,
                                   uint64_t handle, int device)
{
    VmmMappingInfo info{va, size, handle, device};
    std::unique_lock lock(vmm_mu_);
    vmm_mappings_[va] = info;
}

bool GpuTracker::untrack_vmm_mapping(uintptr_t va)
{
    std::unique_lock lock(vmm_mu_);
    return vmm_mappings_.erase(va) > 0;
}

std::vector<VmmMappingInfo> GpuTracker::snapshot_vmm_mappings() const
{
    std::shared_lock lock(vmm_mu_);
    std::vector<VmmMappingInfo> v;
    v.reserve(vmm_mappings_.size());
    for (auto &kv : vmm_mappings_)
        v.push_back(kv.second);
    return v;
}

/* ── Fork safety ─────────────────────────────────────────────────────── */

void GpuTracker::reset_after_fork()
{
    /* After fork(), std::shared_mutex is in undefined state if the parent
     * held any lock. We reconstruct them in-place and clear all tracked data
     * (child process starts with a clean GPU state). */
    new (&alloc_mu_)  std::shared_mutex();
    new (&stream_mu_) std::shared_mutex();
    new (&event_mu_)  std::shared_mutex();
    new (&nccl_mu_)   std::shared_mutex();
    new (&vmm_mu_)    std::shared_mutex();

    allocs_.clear();
    streams_.clear();
    events_.clear();
    nccl_comms_.clear();
    vmm_mappings_.clear();

    next_seq_num_.store(0, std::memory_order_relaxed);
    total_allocated_bytes_.store(0, std::memory_order_relaxed);
    total_kernel_launches_.store(0, std::memory_order_relaxed);
    total_memcpy_bytes_.store(0, std::memory_order_relaxed);
}

/* ── Atomic counters ─────────────────────────────────────────────────── */

void GpuTracker::add_allocated_bytes(int64_t delta)
{
    if (delta > 0) {
        total_allocated_bytes_.fetch_add((uint64_t)delta,
                                         std::memory_order_relaxed);
    }
    /* Decrement is handled in untrack_alloc for correctness. */
}

void GpuTracker::inc_kernel_launches()
{
    total_kernel_launches_.fetch_add(1, std::memory_order_relaxed);
}

void GpuTracker::add_memcpy_bytes(uint64_t bytes)
{
    total_memcpy_bytes_.fetch_add(bytes, std::memory_order_relaxed);
}

GpuStats GpuTracker::get_stats() const
{
    GpuStats s{};
    s.total_allocated_bytes = total_allocated_bytes_.load(std::memory_order_relaxed);
    s.total_kernel_launches = total_kernel_launches_.load(std::memory_order_relaxed);
    s.total_memcpy_bytes    = total_memcpy_bytes_.load(std::memory_order_relaxed);

    {
        std::shared_lock lock(alloc_mu_);
        s.live_alloc_count = allocs_.size();
    }
    {
        std::shared_lock lock(stream_mu_);
        s.live_stream_count = streams_.size();
    }
    {
        std::shared_lock lock(event_mu_);
        s.live_event_count = events_.size();
    }
    {
        std::shared_lock lock(nccl_mu_);
        s.live_nccl_comm_count = nccl_comms_.size();
    }
    {
        std::shared_lock lock(vmm_mu_);
        s.live_vmm_mapping_count = vmm_mappings_.size();
    }
    return s;
}

} /* namespace nvsnap */

/* ═══════════════════════════════════════════════════════════════════════
 * C API — thin wrappers around the singleton
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" {

void nvsnap_tracker_track_alloc(uintptr_t ptr, size_t size, int device, int type)
{
    nvsnap::GpuTracker::instance().track_alloc(
        ptr, size, device, static_cast<nvsnap::AllocType>(type));
}

void nvsnap_tracker_untrack_alloc(uintptr_t ptr)
{
    nvsnap::GpuTracker::instance().untrack_alloc(ptr);
}

void nvsnap_tracker_add_allocated_bytes(int64_t delta)
{
    nvsnap::GpuTracker::instance().add_allocated_bytes(delta);
}

void nvsnap_tracker_inc_kernel_launches(void)
{
    nvsnap::GpuTracker::instance().inc_kernel_launches();
}

void nvsnap_tracker_add_memcpy_bytes(uint64_t bytes)
{
    nvsnap::GpuTracker::instance().add_memcpy_bytes(bytes);
}

void nvsnap_tracker_track_stream(void *handle, int device, unsigned flags)
{
    nvsnap::GpuTracker::instance().track_stream(handle, device, flags);
}

void nvsnap_tracker_untrack_stream(void *handle)
{
    nvsnap::GpuTracker::instance().untrack_stream(handle);
}

void nvsnap_tracker_inc_stream_kernel_count(void *stream)
{
    nvsnap::GpuTracker::instance().inc_stream_kernel_count(stream);
}

void nvsnap_tracker_track_event(void *handle, int device, unsigned flags)
{
    nvsnap::GpuTracker::instance().track_event(handle, device, flags);
}

void nvsnap_tracker_untrack_event(void *handle)
{
    nvsnap::GpuTracker::instance().untrack_event(handle);
}

void nvsnap_tracker_track_nccl_comm(void *comm, int nranks, int rank,
                                      const unsigned char unique_id[128], int device)
{
    nvsnap::GpuTracker::instance().track_nccl_comm(comm, nranks, rank, unique_id, device);
}

void nvsnap_tracker_untrack_nccl_comm(void *comm)
{
    nvsnap::GpuTracker::instance().untrack_nccl_comm(comm);
}

void nvsnap_tracker_record_nccl_collective(void *comm, uint64_t bytes)
{
    nvsnap::GpuTracker::instance().record_nccl_collective(comm, bytes);
}

void nvsnap_tracker_track_vmm_mapping(uintptr_t va, size_t size,
                                        uint64_t handle, int device)
{
    nvsnap::GpuTracker::instance().track_vmm_mapping(va, size, handle, device);
}

void nvsnap_tracker_untrack_vmm_mapping(uintptr_t va)
{
    nvsnap::GpuTracker::instance().untrack_vmm_mapping(va);
}

void nvsnap_tracker_reset_after_fork(void)
{
    nvsnap::GpuTracker::instance().reset_after_fork();
}

} /* extern "C" */

/*
 * NvSnap -- GPU checkpoint/restore implementation.
 *
 * Uses REAL CUDA function pointers (not our wrappers) for all GPU
 * operations during save/restore so we don't re-enter interposition.
 */
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

/* Paths are bounded by PATH_MAX in practice; GCC's static analysis
 * can't prove snprintf(PATH_MAX, "%s/meta.bin", PATH_MAX_input) won't
 * truncate, but real filesystem paths never approach PATH_MAX. */
#pragma GCC diagnostic ignored "-Wformat-truncation"

#include <algorithm>
#include <vector>

#include <dlfcn.h>
#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "nvsnap/gpu/checkpoint.h"
#include "nvsnap/gpu/cuda_types.h"
#include "nvsnap/gpu/interpose.h"
#include "nvsnap/gpu/tracker.h"

/* Defined in interpose_cudart.c */
extern "C" int nvsnap_get_current_device(void);

/* ═══════════════════════════════════════════════════════════════════════
 * Real CUDA function pointers — resolved lazily via dlsym(RTLD_NEXT).
 * These bypass our interposition layer.
 * ═══════════════════════════════════════════════════════════════════════ */

static cudaError_t (*real_ckpt_cudaDeviceSynchronize)(void) = nullptr;
static cudaError_t (*real_ckpt_cudaMemcpy)(void *, const void *, size_t,
                                            cudaMemcpyKind) = nullptr;
static cudaError_t (*real_ckpt_cudaStreamCreate)(cudaStream_t *) = nullptr;
static cudaError_t (*real_ckpt_cudaEventCreateWithFlags)(cudaEvent_t *,
                                                          unsigned) = nullptr;
static CUresult (*real_ckpt_cuInit)(unsigned) = nullptr;
static CUresult (*real_ckpt_cuDevicePrimaryCtxRetain)(CUcontext *, CUdevice) = nullptr;
static CUresult (*real_ckpt_cuMemAddressReserve)(CUdeviceptr *, size_t,
                                                  size_t, CUdeviceptr,
                                                  unsigned long long) = nullptr;
static CUresult (*real_ckpt_cuMemCreate)(CUmemGenericAllocationHandle *,
                                          size_t, const CUmemAllocationProp *,
                                          unsigned long long) = nullptr;
static CUresult (*real_ckpt_cuMemMap)(CUdeviceptr, size_t, size_t,
                                       CUmemGenericAllocationHandle,
                                       unsigned long long) = nullptr;
static CUresult (*real_ckpt_cuMemSetAccess)(CUdeviceptr, size_t,
                                             const CUmemAccessDesc *,
                                             size_t) = nullptr;
static CUresult (*real_ckpt_cuMemGetAllocationGranularity)(
    size_t *, const CUmemAllocationProp *,
    CUmemAllocationGranularity_flags) = nullptr;
static cudaError_t (*real_ckpt_cudaMalloc)(void **, size_t) = nullptr;
static cudaError_t (*real_ckpt_cudaFree)(void *) = nullptr;
static cudaError_t (*real_ckpt_cudaSetDevice)(int) = nullptr;
static const char *(*real_ckpt_cudaGetErrorString)(cudaError_t) = nullptr;

static void force_resolve_real_functions(void);

static void ensure_real_functions(void)
{
    if (real_ckpt_cudaDeviceSynchronize)
        return;

    /* Resolve ALL function pointers with nvsnap_resolve_real fallback.
     * dlsym(RTLD_NEXT) fails for dynamically-loaded libraries (libcuda.so
     * loaded via dlopen by PyTorch). nvsnap_resolve_real searches all
     * loaded libraries and verifies the result isn't our own wrapper. */
/*
 * Resolve REAL CUDA function pointers, bypassing our own wrappers.
 *
 * After CRIU restore, dlsym(RTLD_NEXT) can return our own interposed
 * functions (broken state). nvsnap_resolve_real() explicitly opens
 * the target library and uses dladdr() to verify the result isn't us.
 *
 * Try the explicit library first (most reliable), RTLD_NEXT as fallback.
 */
#define RESOLVE_REAL(var, type, name) do { \
    var = (type)nvsnap_resolve_real(name, "libcudart.so"); \
    if (!var) var = (type)nvsnap_resolve_real(name, "libcuda.so.1"); \
    if (!var) var = (type)dlsym(RTLD_NEXT, name); \
} while (0)

    RESOLVE_REAL(real_ckpt_cudaDeviceSynchronize,
        cudaError_t (*)(void), "cudaDeviceSynchronize");
    RESOLVE_REAL(real_ckpt_cudaMemcpy,
        cudaError_t (*)(void *, const void *, size_t, cudaMemcpyKind), "cudaMemcpy");
    RESOLVE_REAL(real_ckpt_cudaStreamCreate,
        cudaError_t (*)(cudaStream_t *), "cudaStreamCreate");
    RESOLVE_REAL(real_ckpt_cudaEventCreateWithFlags,
        cudaError_t (*)(cudaEvent_t *, unsigned), "cudaEventCreateWithFlags");
    RESOLVE_REAL(real_ckpt_cuInit,
        CUresult (*)(unsigned), "cuInit");
    RESOLVE_REAL(real_ckpt_cuDevicePrimaryCtxRetain,
        CUresult (*)(CUcontext *, CUdevice), "cuDevicePrimaryCtxRetain");

    RESOLVE_REAL(real_ckpt_cuMemAddressReserve,
        CUresult (*)(CUdeviceptr *, size_t, size_t, CUdeviceptr, unsigned long long),
        "cuMemAddressReserve");
    RESOLVE_REAL(real_ckpt_cuMemCreate,
        CUresult (*)(CUmemGenericAllocationHandle *, size_t, const CUmemAllocationProp *, unsigned long long),
        "cuMemCreate");
    RESOLVE_REAL(real_ckpt_cuMemMap,
        CUresult (*)(CUdeviceptr, size_t, size_t, CUmemGenericAllocationHandle, unsigned long long),
        "cuMemMap");
    RESOLVE_REAL(real_ckpt_cuMemSetAccess,
        CUresult (*)(CUdeviceptr, size_t, const CUmemAccessDesc *, size_t),
        "cuMemSetAccess");
    RESOLVE_REAL(real_ckpt_cuMemGetAllocationGranularity,
        CUresult (*)(size_t *, const CUmemAllocationProp *, CUmemAllocationGranularity_flags),
        "cuMemGetAllocationGranularity");
    RESOLVE_REAL(real_ckpt_cudaMalloc,
        cudaError_t (*)(void **, size_t), "cudaMalloc");
    RESOLVE_REAL(real_ckpt_cudaFree,
        cudaError_t (*)(void *), "cudaFree");
    RESOLVE_REAL(real_ckpt_cudaSetDevice,
        cudaError_t (*)(int), "cudaSetDevice");
    RESOLVE_REAL(real_ckpt_cudaGetErrorString,
        const char *(*)(cudaError_t), "cudaGetErrorString");

#undef RESOLVE_REAL
}

/*
 * Force re-resolution of all CUDA function pointers.
 *
 * After CRIU restore, the cached pointers from the original run are
 * stale — CUDA libraries may be at different addresses (ASLR).
 * This clears all cached pointers and re-resolves via dlsym.
 */
static void force_resolve_real_functions(void)
{
    real_ckpt_cudaDeviceSynchronize = nullptr;
    real_ckpt_cudaMemcpy = nullptr;
    real_ckpt_cudaStreamCreate = nullptr;
    real_ckpt_cudaEventCreateWithFlags = nullptr;
    real_ckpt_cuInit = nullptr;
    real_ckpt_cuDevicePrimaryCtxRetain = nullptr;
    real_ckpt_cuMemAddressReserve = nullptr;
    real_ckpt_cuMemCreate = nullptr;
    real_ckpt_cuMemMap = nullptr;
    real_ckpt_cuMemSetAccess = nullptr;
    real_ckpt_cuMemGetAllocationGranularity = nullptr;
    real_ckpt_cudaMalloc = nullptr;
    real_ckpt_cudaGetErrorString = nullptr;
    real_ckpt_cudaFree = nullptr;
    real_ckpt_cudaSetDevice = nullptr;

    ensure_real_functions();

    int resolved = 0;
    if (real_ckpt_cudaDeviceSynchronize) resolved++;
    if (real_ckpt_cudaMemcpy) resolved++;
    if (real_ckpt_cuInit) resolved++;
    if (real_ckpt_cuDevicePrimaryCtxRetain) resolved++;
    if (real_ckpt_cuMemAddressReserve) resolved++;
    if (real_ckpt_cuMemCreate) resolved++;
    if (real_ckpt_cuMemMap) resolved++;
    if (real_ckpt_cuMemSetAccess) resolved++;
    if (real_ckpt_cuMemGetAllocationGranularity) resolved++;
    if (real_ckpt_cudaMalloc) resolved++;
    if (real_ckpt_cudaFree) resolved++;
    if (real_ckpt_cudaSetDevice) resolved++;

    /* Log where cudaMalloc resolved from — critical for debugging self-resolution */
    if (real_ckpt_cudaMalloc) {
        Dl_info minfo = {};
        if (dladdr((void *)real_ckpt_cudaMalloc, &minfo)) {
            NVSNAP_GPU_LOG_INFO("checkpoint: cudaMalloc resolved from %s",
                              minfo.dli_fname ? minfo.dli_fname : "unknown");
        }
    }

    NVSNAP_GPU_LOG_INFO("checkpoint: re-resolved %d/12 CUDA function pointers "
                      "(cuInit=%p, cudaMalloc=%p, cuMemAddressReserve=%p)",
                      resolved,
                      (void *)real_ckpt_cuInit,
                      (void *)real_ckpt_cudaMalloc,
                      (void *)real_ckpt_cuMemAddressReserve);
}

/* ═══════════════════════════════════════════════════════════════════════
 * Helpers
 * ═══════════════════════════════════════════════════════════════════════ */

static uint64_t now_ns(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
}

static int write_all(const void *buf, size_t size, FILE *fp)
{
    return fwrite(buf, 1, size, fp) == size ? 0 : -1;
}

static int mkdirs(const char *path)
{
    /* Try mkdir; if it fails because parent doesn't exist, create parents. */
    if (mkdir(path, 0755) == 0 || errno == EEXIST)
        return 0;

    /* Simple recursive mkdir. */
    char tmp[PATH_MAX];
    snprintf(tmp, sizeof(tmp), "%s", path);
    for (char *p = tmp + 1; *p; p++) {
        if (*p == '/') {
            *p = '\0';
            mkdir(tmp, 0755);
            *p = '/';
        }
    }
    return mkdir(tmp, 0755) == 0 || errno == EEXIST ? 0 : -1;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Checkpoint Save
 * ═══════════════════════════════════════════════════════════════════════ */

/*
 * Reset the deferred restore state so that after CRIU checkpoint+restore,
 * the restored process will re-check for the marker file.
 * Defined in interpose_cudart.c.
 */
extern void nvsnap_reset_restore_state(void);

extern "C" int nvsnap_checkpoint_save(const char *checkpoint_dir)
{
    if (!checkpoint_dir) {
        NVSNAP_GPU_LOG_ERROR("checkpoint_save: NULL directory");
        return -1;
    }

    ensure_real_functions();

    /* Build per-process save directory: <dir>/gpu-<pid>/ */
    char save_dir[PATH_MAX];
    snprintf(save_dir, sizeof(save_dir), "%s/gpu-%d",
             checkpoint_dir, (int)getpid());

    /* Early exit: if this process has no GPU allocations, skip save entirely.
     * Non-GPU processes (uvicorn workers, Python threads) call this but have
     * nothing to save. Calling cudaDeviceSynchronize in a non-GPU process
     * either hangs (CUDA not initialized) or wastes time. */
    {
        auto &tracker = nvsnap::GpuTracker::instance();
        if (tracker.snapshot_allocs().empty()) {
            NVSNAP_GPU_LOG_INFO("checkpoint: 0 allocations tracked, skipping save (pid=%d)",
                              (int)getpid());
            return 0;
        }
    }

    NVSNAP_GPU_LOG_INFO("checkpoint: saving to %s", save_dir);

    /* Create output directory. */
    if (mkdirs(save_dir) != 0) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cannot create directory %s: %s",
                           save_dir, strerror(errno));
        return -1;
    }

    /* 1. Quiesce all GPU operations. */
    if (real_ckpt_cudaDeviceSynchronize) {
        cudaError_t err = real_ckpt_cudaDeviceSynchronize();
        if (err != cudaSuccess) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: cudaDeviceSynchronize failed: %d",
                               (int)err);
            return -1;
        }
    }

    /* 2. Snapshot tracker state. */
    auto &tracker = nvsnap::GpuTracker::instance();
    auto allocs = tracker.snapshot_allocs();
    auto streams = tracker.snapshot_streams();
    auto nccl_comms = tracker.snapshot_nccl_comms();
    auto stats = tracker.get_stats();

    /* 3. Open output files. */
    char meta_path[PATH_MAX], data_path[PATH_MAX];
    snprintf(meta_path, sizeof(meta_path), "%s/meta.bin", save_dir);
    snprintf(data_path, sizeof(data_path), "%s/gpu_data.bin", save_dir);

    FILE *meta_fp = fopen(meta_path, "wb");
    if (!meta_fp) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cannot open %s: %s", meta_path,
                           strerror(errno));
        return -1;
    }

    FILE *data_fp = fopen(data_path, "wb");
    if (!data_fp) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cannot open %s: %s", data_path,
                           strerror(errno));
        fclose(meta_fp);
        return -1;
    }

    /* 4. Prepare and write header. */
    NvSnapCheckpointHeader header = {};
    header.magic = NVSNAP_CHECKPOINT_MAGIC;
    header.version = NVSNAP_CHECKPOINT_VERSION;
    header.num_allocations = (uint32_t)allocs.size();
    header.num_streams = (uint32_t)streams.size();
    header.num_events = (uint32_t)stats.live_event_count;
    header.num_nccl_comms = (uint32_t)nccl_comms.size();
    header.num_vmm_mappings = (uint32_t)stats.live_vmm_mapping_count;
    /* Use the device from the first allocation, not nvsnap_current_device
     * which may be wrong if save is called from a non-CUDA thread. */
    header.source_device = allocs.empty() ? (uint32_t)nvsnap_current_device
                                          : (uint32_t)allocs[0].device;
    header.timestamp = now_ns();

    /* Calculate total GPU bytes. */
    uint64_t total_bytes = 0;
    for (auto &a : allocs) {
        /* Only save DEVICE and MANAGED allocations (skip HOST_PINNED). */
        if (a.type == nvsnap::AllocType::DEVICE ||
            a.type == nvsnap::AllocType::MANAGED) {
            total_bytes += a.size;
        }
    }
    header.total_gpu_bytes = total_bytes;

    if (write_all(&header, sizeof(header), meta_fp) != 0) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: failed to write header");
        fclose(meta_fp);
        fclose(data_fp);
        return -1;
    }

    /* 5. For each allocation, save GPU data and build metadata records. */
    std::vector<NvSnapCheckpointAlloc> alloc_recs(allocs.size());
    uint64_t data_offset = 0;
    for (size_t i = 0; i < allocs.size(); i++) {
        auto &a = allocs[i];
        NvSnapCheckpointAlloc &rec = alloc_recs[i];
        memset(&rec, 0, sizeof(rec));
        rec.ptr = (uint64_t)a.ptr;
        rec.size = (uint64_t)a.size;
        rec.device = (int32_t)a.device;
        rec.alloc_type = (uint32_t)a.type;
        rec.seq_num = a.seq_num;

        bool save_data = (a.type == nvsnap::AllocType::DEVICE ||
                          a.type == nvsnap::AllocType::MANAGED);

        if (save_data) {
            rec.data_offset = data_offset;

            /* Allocate host staging buffer and copy D2H. */
            void *host_buf = malloc(a.size);
            if (!host_buf) {
                NVSNAP_GPU_LOG_ERROR("checkpoint: malloc(%zu) failed for ptr 0x%lx",
                                   a.size, (unsigned long)a.ptr);
                fclose(meta_fp);
                fclose(data_fp);
                return -1;
            }

            if (real_ckpt_cudaMemcpy) {
                cudaError_t err = real_ckpt_cudaMemcpy(
                    host_buf, (const void *)a.ptr, a.size,
                    cudaMemcpyDeviceToHost);
                if (err != cudaSuccess) {
                    NVSNAP_GPU_LOG_ERROR(
                        "checkpoint: cudaMemcpy D2H failed for ptr 0x%lx: %d",
                        (unsigned long)a.ptr, (int)err);
                    free(host_buf);
                    fclose(meta_fp);
                    fclose(data_fp);
                    return -1;
                }
            }

            if (write_all(host_buf, a.size, data_fp) != 0) {
                NVSNAP_GPU_LOG_ERROR("checkpoint: failed to write gpu data");
                free(host_buf);
                fclose(meta_fp);
                fclose(data_fp);
                return -1;
            }
            free(host_buf);

            data_offset += a.size;
        } else {
            rec.data_offset = UINT64_MAX; /* sentinel: no GPU data */
        }
    }

    /* Sort allocation records by seq_num for deterministic replay order. */
    std::sort(alloc_recs.begin(), alloc_recs.end(),
              [](const NvSnapCheckpointAlloc &a,
                 const NvSnapCheckpointAlloc &b) {
                  return a.seq_num < b.seq_num;
              });

    for (auto &rec : alloc_recs)
        (void)write_all(&rec, sizeof(rec), meta_fp);

    /* 6. Write stream metadata. */
    for (auto &s : streams) {
        NvSnapCheckpointStream srec = {};
        srec.handle = (uint64_t)(uintptr_t)s.handle;
        srec.device = (int32_t)s.device;
        srec.flags = s.flags;
        (void)write_all(&srec, sizeof(srec), meta_fp);
    }

    /* 7. Write NCCL comm metadata. */
    for (auto &c : nccl_comms) {
        NvSnapCheckpointNcclComm crec = {};
        crec.comm = (uint64_t)(uintptr_t)c.comm;
        crec.nranks = (int32_t)c.nranks;
        crec.rank = (int32_t)c.rank;
        memcpy(crec.unique_id, c.unique_id, 128);
        crec.device = (int32_t)c.device;
        (void)write_all(&crec, sizeof(crec), meta_fp);
    }

    fclose(meta_fp);
    fclose(data_fp);

    NVSNAP_GPU_LOG_INFO("checkpoint: saved %u allocs (%lu bytes), "
                      "%u streams, %u NCCL comms to %s",
                      header.num_allocations,
                      (unsigned long)header.total_gpu_bytes,
                      header.num_streams, header.num_nccl_comms,
                      save_dir);

    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Restore helpers
 * ═══════════════════════════════════════════════════════════════════════ */

/*
 * VMM-based restore (fallback). Uses cuMemAddressReserve + cuMemCreate + cuMemMap.
 * Returns:
 *   0  — restored at original VA
 *   1  — restored at fallback VA (VA not preserved)
 *  -1  — failed to restore
 */
__attribute__((unused))
static int restore_one_alloc_vmm(NvSnapCheckpointAlloc *rec, FILE *data_fp,
                                  nvsnap::GpuTracker &tracker)
{
    CUmemAllocationProp prop = {};
    prop.type = CU_MEM_ALLOCATION_TYPE_PINNED;
    prop.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    prop.location.id = rec->device;

    NVSNAP_GPU_LOG_DEBUG("restore_one_alloc: ptr=0x%lx size=%lu device=%d",
                       (unsigned long)rec->ptr, (unsigned long)rec->size,
                       rec->device);

    size_t granularity = 0;
    CUresult gres = real_ckpt_cuMemGetAllocationGranularity(
        &granularity, &prop, CU_MEM_ALLOC_GRANULARITY_MINIMUM);
    if (gres != CUDA_SUCCESS || granularity == 0)
        granularity = 2 * 1024 * 1024; /* 2 MiB default */

    /* Round size up to granularity. */
    size_t alloc_size =
        ((rec->size + granularity - 1) / granularity) * granularity;

    /* Try to reserve at the original VA first. */
    CUdeviceptr reserved_va = 0;
    int va_preserved = 1;
    CUresult res = real_ckpt_cuMemAddressReserve(
        &reserved_va, alloc_size, granularity, (CUdeviceptr)rec->ptr, 0);
    if (res != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_WARN(
            "checkpoint: cuMemAddressReserve at original VA 0x%lx (size %lu) "
            "failed: %d, trying fallback VA",
            (unsigned long)rec->ptr, (unsigned long)rec->size, (int)res);

        /* Fallback: reserve at any available VA. */
        reserved_va = 0;
        res = real_ckpt_cuMemAddressReserve(
            &reserved_va, alloc_size, granularity, 0, 0);
        if (res != CUDA_SUCCESS) {
            NVSNAP_GPU_LOG_ERROR(
                "checkpoint: cuMemAddressReserve fallback also failed for "
                "0x%lx (size %lu): %d",
                (unsigned long)rec->ptr, (unsigned long)rec->size, (int)res);
            return -1;
        }
        NVSNAP_GPU_LOG_WARN(
            "checkpoint: alloc 0x%lx restored at fallback VA 0x%lx "
            "(VA NOT preserved)",
            (unsigned long)rec->ptr, (unsigned long)reserved_va);
        va_preserved = 0;
    }

    CUmemGenericAllocationHandle handle = 0;
    res = real_ckpt_cuMemCreate(&handle, alloc_size, &prop, 0);
    if (res != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuMemCreate failed for 0x%lx "
                           "(size=%lu, device=%d): %d",
                           (unsigned long)rec->ptr, (unsigned long)alloc_size,
                           rec->device, (int)res);
        return -1;
    }
    NVSNAP_GPU_LOG_DEBUG("checkpoint: cuMemCreate OK handle=0x%lx size=%lu",
                       (unsigned long)handle, (unsigned long)alloc_size);

    res = real_ckpt_cuMemMap(reserved_va, alloc_size, 0, handle, 0);
    if (res != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuMemMap(va=0x%lx, size=%lu, handle=0x%lx) "
                           "failed: %d",
                           (unsigned long)reserved_va, (unsigned long)alloc_size,
                           (unsigned long)handle, (int)res);
        return -1;
    }
    NVSNAP_GPU_LOG_DEBUG("checkpoint: cuMemMap OK va=0x%lx", (unsigned long)reserved_va);

    CUmemAccessDesc access = {};
    access.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    access.location.id = rec->device;
    access.flags = CU_MEM_ACCESS_FLAGS_PROT_READWRITE;
    res = real_ckpt_cuMemSetAccess(reserved_va, alloc_size, &access, 1);
    if (res != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_ERROR(
            "checkpoint: cuMemSetAccess(va=0x%lx, size=%lu, device=%d) failed: %d",
            (unsigned long)reserved_va, (unsigned long)alloc_size,
            rec->device, (int)res);
        return -1;
    }
    NVSNAP_GPU_LOG_DEBUG("checkpoint: cuMemSetAccess OK va=0x%lx", (unsigned long)reserved_va);

    /* Copy data from file to GPU. */
    void *host_buf = malloc(rec->size);
    if (!host_buf) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: malloc(%lu) failed",
                           (unsigned long)rec->size);
        return -1;
    }

    if (fseek(data_fp, (long)rec->data_offset, SEEK_SET) != 0 ||
        fread(host_buf, 1, rec->size, data_fp) != rec->size) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: short read on gpu_data.bin");
        free(host_buf);
        return -1;
    }

    if (real_ckpt_cudaMemcpy) {
        cudaError_t merr = real_ckpt_cudaMemcpy(
            (void *)reserved_va, host_buf, rec->size,
            cudaMemcpyHostToDevice);
        if (merr != cudaSuccess) {
            const char *errstr = (real_ckpt_cudaGetErrorString)
                                     ? real_ckpt_cudaGetErrorString(merr)
                                     : "<cudaGetErrorString unresolved>";
            NVSNAP_GPU_LOG_ERROR(
                "checkpoint: H2D memcpy failed for 0x%lx: %d (%s)",
                (unsigned long)rec->ptr, (int)merr, errstr ? errstr : "<null>");
            free(host_buf);
            return -1;
        }
    }
    free(host_buf);

    /* Re-register in tracker. */
    tracker.track_alloc((uintptr_t)reserved_va, rec->size, rec->device,
                        static_cast<nvsnap::AllocType>(rec->alloc_type));

    return va_preserved ? 0 : 1;
}

/*
 * cudaMalloc-replay restore (primary path, post-CRIU).
 * After CRIU restore, VMM APIs fail (error 304) but cudaMalloc works.
 * Replaying allocations in seq_num order produces deterministic VAs.
 *
 * Returns:
 *   0  — restored, VA matches original
 *  -2  — VA mismatch (cudaMalloc succeeded but at wrong address)
 *  -1  — hard error
 */
static int restore_one_alloc(NvSnapCheckpointAlloc *rec, FILE *data_fp,
                              nvsnap::GpuTracker &tracker)
{
    /*
     * After cuda-checkpoint resume, the original GPU allocations already
     * exist at their original VAs. The RM state was restored — the memory
     * is allocated and mapped. We just need to copy the data back.
     *
     * NO cudaMalloc needed. The VAs are already valid.
     */

    /* 1. Skip allocations with no saved data. */
    if (rec->data_offset == UINT64_MAX) {
        tracker.track_alloc((uintptr_t)rec->ptr, rec->size, rec->device,
                            static_cast<nvsnap::AllocType>(rec->alloc_type));
        return 0;
    }

    /* 2. Read data from checkpoint file into host buffer. */
    void *host_buf = malloc((size_t)rec->size);
    if (!host_buf) {
        NVSNAP_GPU_LOG_ERROR("restore: host malloc(%lu) failed", (unsigned long)rec->size);
        return -1;
    }

    if (fseek(data_fp, (long)rec->data_offset, SEEK_SET) != 0 ||
        fread(host_buf, 1, (size_t)rec->size, data_fp) != (size_t)rec->size) {
        NVSNAP_GPU_LOG_ERROR("restore: short read for alloc seq=%u", rec->seq_num);
        free(host_buf);
        return -1;
    }

    /* 3. H2D copy to the ORIGINAL VA (already allocated by cuda-checkpoint resume). */
    cudaError_t merr = real_ckpt_cudaMemcpy((void *)(uintptr_t)rec->ptr,
                                             host_buf, (size_t)rec->size,
                                             cudaMemcpyHostToDevice);
    free(host_buf);
    if (merr != 0) {
        const char *errstr = (real_ckpt_cudaGetErrorString)
                                 ? real_ckpt_cudaGetErrorString(merr)
                                 : "<cudaGetErrorString unresolved>";
        NVSNAP_GPU_LOG_ERROR("restore: H2D to 0x%lx size=%lu failed: %d (%s)",
                           (unsigned long)rec->ptr, (unsigned long)rec->size,
                           (int)merr, errstr ? errstr : "<null>");
        return -1;
    }

    /* 4. Re-register in tracker at the original VA. */
    tracker.track_alloc((uintptr_t)rec->ptr, rec->size, rec->device,
                        static_cast<nvsnap::AllocType>(rec->alloc_type));

    NVSNAP_GPU_LOG_INFO("restore: seq=%u ptr=0x%lx size=%lu H2D OK",
                      rec->seq_num, (unsigned long)rec->ptr,
                      (unsigned long)rec->size);
    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Checkpoint Restore
 * ═══════════════════════════════════════════════════════════════════════ */

/* Activate a specific GPU device for VMM operations. */
static int activate_gpu_device(int device)
{
    /* Always re-resolve — after CRIU restore, static locals are stale. */
    CUresult (*real_cuCtxSetCurrent)(CUcontext) = nullptr;
    real_cuCtxSetCurrent = (CUresult (*)(CUcontext))
        dlsym(RTLD_NEXT, "cuCtxSetCurrent");
    if (!real_cuCtxSetCurrent)
        real_cuCtxSetCurrent = (CUresult (*)(CUcontext))
            nvsnap_resolve_real("cuCtxSetCurrent", "libcuda.so.1");

    if (!real_ckpt_cuDevicePrimaryCtxRetain) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuDevicePrimaryCtxRetain not resolved");
        return -1;
    }
    if (!real_cuCtxSetCurrent) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuCtxSetCurrent not resolved");
        return -1;
    }

    CUcontext ctx = nullptr;
    CUresult cres = real_ckpt_cuDevicePrimaryCtxRetain(&ctx, (CUdevice)device);
    if (cres != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuDevicePrimaryCtxRetain(device=%d) failed: %d",
                           device, (int)cres);
        return -1;
    }

    cres = real_cuCtxSetCurrent(ctx);
    if (cres != CUDA_SUCCESS) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cuCtxSetCurrent failed: %d", (int)cres);
        return -1;
    }

    NVSNAP_GPU_LOG_INFO("checkpoint: activated GPU context for device %d", device);
    return 0;
}

/*
 * Restore one gpu-<pid>/ subdirectory.
 * Returns 0 on success, -2 on VA mismatches, -1 on hard error.
 */
static int restore_one_gpu_dir(const char *restore_dir)
{
    NVSNAP_GPU_LOG_INFO("checkpoint: restoring from %s", restore_dir);

    char meta_path[PATH_MAX], data_path[PATH_MAX];
    snprintf(meta_path, sizeof(meta_path), "%s/meta.bin", restore_dir);
    snprintf(data_path, sizeof(data_path), "%s/gpu_data.bin", restore_dir);

    FILE *meta_fp = fopen(meta_path, "rb");
    if (!meta_fp) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cannot open %s: %s", meta_path,
                           strerror(errno));
        return -1;
    }

    /* 2. Read and validate header. */
    NvSnapCheckpointHeader header = {};
    if (fread(&header, sizeof(header), 1, meta_fp) != 1) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: failed to read header from %s",
                           meta_path);
        fclose(meta_fp);
        return -1;
    }

    /* Skip empty checkpoints (non-GPU processes). */
    if (header.num_allocations == 0) {
        NVSNAP_GPU_LOG_INFO("checkpoint: %s has 0 allocations, skipping", restore_dir);
        fclose(meta_fp);
        return 0;
    }

    /* Determine the correct GPU device for this checkpoint.
     *
     * Primary source: header.source_device — set during checkpoint and
     * records which GPU each worker was actually using.
     *
     * cudaGetDevice() is NOT reliable after CRIU restore: it returns 0
     * for all processes regardless of which device they were using. */
    int device_to_use = (int)header.source_device;
    {
        /* Override with LOCAL_RANK if set (torchrun/vLLM). */
        const char *lr = getenv("LOCAL_RANK");
        if (lr) device_to_use = atoi(lr);

        NVSNAP_GPU_LOG_INFO("checkpoint: %s: using device %d "
                          "(header=%u, LOCAL_RANK=%s)",
                          restore_dir, device_to_use,
                          header.source_device,
                          getenv("LOCAL_RANK") ? getenv("LOCAL_RANK") : "unset");

        if (activate_gpu_device(device_to_use) < 0) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: failed to activate device %d for %s",
                               device_to_use, restore_dir);
            fclose(meta_fp);
            return -1;
        }
    }

    if (header.magic != NVSNAP_CHECKPOINT_MAGIC) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: bad magic 0x%08x (expected 0x%08x)",
                           header.magic, NVSNAP_CHECKPOINT_MAGIC);
        fclose(meta_fp);
        return -1;
    }

    if (header.version != NVSNAP_CHECKPOINT_VERSION) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: unsupported version %u (expected %u)",
                           header.version, NVSNAP_CHECKPOINT_VERSION);
        fclose(meta_fp);
        return -1;
    }

    /* 3. Read allocation records. */
    auto *alloc_recs = (NvSnapCheckpointAlloc *)calloc(
        header.num_allocations, sizeof(NvSnapCheckpointAlloc));
    if (header.num_allocations > 0 && !alloc_recs) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: failed to allocate alloc records");
        fclose(meta_fp);
        return -1;
    }
    if (header.num_allocations > 0) {
        size_t n = fread(alloc_recs, sizeof(NvSnapCheckpointAlloc),
                         header.num_allocations, meta_fp);
        if (n != header.num_allocations) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: short read on alloc records");
            free(alloc_recs);
            fclose(meta_fp);
            return -1;
        }
    }

    /* 4. Read stream records. */
    auto *stream_recs = (NvSnapCheckpointStream *)calloc(
        header.num_streams, sizeof(NvSnapCheckpointStream));
    if (header.num_streams > 0) {
        if (!stream_recs ||
            fread(stream_recs, sizeof(NvSnapCheckpointStream),
                  header.num_streams, meta_fp) != header.num_streams) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: failed to read stream records");
            free(alloc_recs);
            free(stream_recs);
            fclose(meta_fp);
            return -1;
        }
    }

    /* 5. Read NCCL comm records. */
    auto *nccl_recs = (NvSnapCheckpointNcclComm *)calloc(
        header.num_nccl_comms, sizeof(NvSnapCheckpointNcclComm));
    if (header.num_nccl_comms > 0) {
        if (!nccl_recs ||
            fread(nccl_recs, sizeof(NvSnapCheckpointNcclComm),
                  header.num_nccl_comms, meta_fp) != header.num_nccl_comms) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: failed to read NCCL records");
            free(alloc_recs);
            free(stream_recs);
            free(nccl_recs);
            fclose(meta_fp);
            return -1;
        }
    }

    fclose(meta_fp);

    /* 6. Open GPU data file. */
    FILE *data_fp = nullptr;
    if (header.total_gpu_bytes > 0) {
        data_fp = fopen(data_path, "rb");
        if (!data_fp) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: cannot open %s: %s", data_path,
                               strerror(errno));
            free(alloc_recs);
            free(stream_recs);
            free(nccl_recs);
            return -1;
        }
    }

    /* 7. Restore allocations via cudaMalloc replay for deterministic VAs.
     *    Records are already sorted by seq_num in meta.bin (#68).
     *    cudaSetDevice selects the target GPU for all subsequent cudaMalloc calls. */
    auto &tracker = nvsnap::GpuTracker::instance();

    int va_mismatches = 0;
    int restore_errors = 0;

    /* Set the CUDA runtime device for H2D copies. */
    if (real_ckpt_cudaSetDevice) {
        NVSNAP_GPU_LOG_INFO("checkpoint: cudaSetDevice(%d)...", device_to_use);
        cudaError_t sderr = real_ckpt_cudaSetDevice(device_to_use);
        NVSNAP_GPU_LOG_INFO("checkpoint: cudaSetDevice(%d) = %d", device_to_use, (int)sderr);
        if (sderr != 0) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: cudaSetDevice(%d) failed: %d",
                               device_to_use, (int)sderr);
            if (data_fp) fclose(data_fp);
            free(alloc_recs);
            free(stream_recs);
            free(nccl_recs);
            return -1;
        }
    }

    for (uint32_t i = 0; i < header.num_allocations; i++) {
        NvSnapCheckpointAlloc *rec = &alloc_recs[i];

        /* Override the per-allocation device with the correct one.
         * The saved device field may be wrong (same bug as header). */
        if (i == 0) {
            NVSNAP_GPU_LOG_INFO("checkpoint: overriding alloc device %d -> %d "
                              "for %u allocations",
                              rec->device, device_to_use,
                              header.num_allocations);
        }
        rec->device = (int32_t)device_to_use;

        bool has_data = (rec->data_offset != UINT64_MAX);
        if (!has_data)
            continue;

        if (!real_ckpt_cudaMemcpy) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: cudaMemcpy not resolved — cannot H2D");
            restore_errors++;
            continue;
        }

        int rr = restore_one_alloc(rec, data_fp, tracker);
        if (rr == 1) {
            va_mismatches++;  /* data restored but VA differs */
        } else if (rr < 0) {
            restore_errors++;  /* hard error — allocation lost */
        }
    }

    /* 8. Recreate streams. */
    for (uint32_t i = 0; i < header.num_streams; i++) {
        if (real_ckpt_cudaStreamCreate) {
            cudaStream_t new_stream = nullptr;
            cudaError_t err = real_ckpt_cudaStreamCreate(&new_stream);
            if (err == cudaSuccess && new_stream) {
                tracker.track_stream(new_stream, stream_recs[i].device,
                                     stream_recs[i].flags);
            }
        }
    }

    /* 9. Recreate events. */
    for (uint32_t i = 0; i < header.num_events; i++) {
        if (real_ckpt_cudaEventCreateWithFlags) {
            cudaEvent_t new_event = nullptr;
            cudaError_t err =
                real_ckpt_cudaEventCreateWithFlags(&new_event, 0);
            if (err == cudaSuccess && new_event) {
                tracker.track_event(new_event, header.source_device, 0);
            }
        }
    }

    if (data_fp)
        fclose(data_fp);

    free(alloc_recs);
    free(stream_recs);
    free(nccl_recs);

    NVSNAP_GPU_LOG_INFO("checkpoint: restored %u allocs (%lu bytes), "
                      "%u streams from %s (va_mismatches=%d, errors=%d)",
                      header.num_allocations,
                      (unsigned long)header.total_gpu_bytes,
                      header.num_streams, restore_dir,
                      va_mismatches, restore_errors);

    if (restore_errors > 0)
        return -1;
    if (va_mismatches > 0)
        return 1; /* VA mismatches — data restored but pointers differ */
    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Checkpoint Restore — iterate all gpu-* subdirs
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" int nvsnap_checkpoint_restore(const char *checkpoint_dir)
{
    if (!checkpoint_dir) {
        NVSNAP_GPU_LOG_ERROR("checkpoint_restore: NULL directory");
        return -1;
    }

    force_resolve_real_functions();

    /* Initialize CUDA driver. */
    if (real_ckpt_cuInit) {
        CUresult ires = real_ckpt_cuInit(0);
        if (ires != CUDA_SUCCESS) {
            NVSNAP_GPU_LOG_ERROR("checkpoint: cuInit(0) failed: %d", (int)ires);
            return -1;
        }
    }

    /*
     * Scan checkpoint_dir for gpu-* subdirectories containing meta.bin.
     * Each subdir holds one GPU's allocations from the original process.
     * The restore helper runs as a single process restoring ALL GPUs.
     */
    DIR *dir = opendir(checkpoint_dir);
    if (!dir) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: cannot open directory %s: %s",
                           checkpoint_dir, strerror(errno));
        return -1;
    }

    int total_restored = 0;
    int total_errors = 0;
    int total_va_failures = 0;

    struct dirent *ent;
    while ((ent = readdir(dir)) != NULL) {
        /* Match gpu-* directories. */
        if (strncmp(ent->d_name, "gpu-", 4) != 0)
            continue;

        /* Check if meta.bin exists in this subdir. */
        char subdir[PATH_MAX];
        snprintf(subdir, sizeof(subdir), "%s/%s", checkpoint_dir, ent->d_name);

        char meta_check[PATH_MAX];
        snprintf(meta_check, sizeof(meta_check), "%s/meta.bin", subdir);
        if (access(meta_check, R_OK) != 0)
            continue;

        int rr = restore_one_gpu_dir(subdir);
        if (rr == 1) {
            total_va_failures++;
            NVSNAP_GPU_LOG_ERROR("checkpoint: VA mismatches restoring %s", subdir);
        } else if (rr < 0) {
            total_errors++;
            NVSNAP_GPU_LOG_ERROR("checkpoint: failed to restore %s", subdir);
        }
        total_restored++;
    }
    closedir(dir);

    NVSNAP_GPU_LOG_INFO("checkpoint: restored %d GPU subdirs (%d errors, %d VA failures)",
                      total_restored, total_errors, total_va_failures);

    if (total_restored == 0) {
        NVSNAP_GPU_LOG_ERROR("checkpoint: no gpu-*/meta.bin found in %s", checkpoint_dir);
        return -1;
    }
    if (total_errors > 0)
        return -1;
    if (total_va_failures > 0)
        return 1;
    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Checkpoint Restore — per-process (called from inside restored process)
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" int nvsnap_checkpoint_restore_self(const char *checkpoint_dir)
{
    /* Ensure NvSnap logging is enabled during restore — log_level may be 0
     * (default) if the original process never set NVSNAP_GPU_LOG_LEVEL. */
    if (nvsnap_gpu_log_level < NVSNAP_GPU_LOG_INFO)
        nvsnap_gpu_log_level = NVSNAP_GPU_LOG_INFO;

    NVSNAP_GPU_LOG_INFO("checkpoint_restore_self: ENTERED dir=%s pid=%d",
                      checkpoint_dir ? checkpoint_dir : "NULL", (int)getpid());

    if (!checkpoint_dir) {
        NVSNAP_GPU_LOG_ERROR("checkpoint_restore_self: NULL directory");
        return -1;
    }

    /* Force re-resolve ALL function pointers. After CRIU restore, cached
     * pointers from the original run are stale (ASLR, library relocation). */
    force_resolve_real_functions();

    /* DO NOT call cuInit(0) here. After CRIU restore, the CUDA driver state
     * is preserved (process has open /dev/nvidia* fds, RM objects exist).
     * Calling cuInit would try to RE-initialize, which can fail or destroy
     * the existing state. The restored process already has a working CUDA
     * context from before checkpoint. Just re-resolve functions and proceed
     * with H2D copy. */

    /* Build per-PID path. Only restore THIS process's GPU data.
     * DO NOT fall back to iterating all subdirs — that would attempt
     * H2D to VAs owned by OTHER processes (different CUDA contexts). */
    char restore_dir[PATH_MAX];
    snprintf(restore_dir, sizeof(restore_dir), "%s/gpu-%d",
             checkpoint_dir, (int)getpid());

    char meta_check[PATH_MAX];
    snprintf(meta_check, sizeof(meta_check), "%s/meta.bin", restore_dir);

    if (access(meta_check, R_OK) != 0) {
        NVSNAP_GPU_LOG_INFO("checkpoint: gpu-%d/ not found — this process has no GPU data to restore",
                          (int)getpid());
        return 0; /* Not an error — non-GPU processes have no data */
    }

    NVSNAP_GPU_LOG_INFO("checkpoint: restore_self restoring gpu-%d/", (int)getpid());
    int ret = restore_one_gpu_dir(restore_dir);
    NVSNAP_GPU_LOG_INFO("checkpoint: restore_self result=%d", ret);
    return ret;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Pre-checkpoint quiesce: destroy NCCL + disable P2P
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" int nvsnap_pre_checkpoint_quiesce(void)
{
    /* Early exit for non-GPU processes. */
    /* This function exists for multi-GPU: abort NCCL comms + disable P2P.
     * Skip entirely if no NCCL comms — avoids cudaDeviceSynchronize hanging
     * on pending CUDA graphs/async ops (single-GPU TinyLlama, non-TP workers). */
    auto &tracker_check = nvsnap::GpuTracker::instance();
    if (tracker_check.snapshot_nccl_comms().empty()) {
        NVSNAP_GPU_LOG_INFO("quiesce: no NCCL comms, skipping pre-checkpoint (pid=%d)", (int)getpid());
        return 0;
    }

    NVSNAP_GPU_LOG_INFO("quiesce: starting pre-checkpoint cleanup (pid=%d)", (int)getpid());

    /* Resolve real CUDA functions (not our wrappers). */
    static cudaError_t (*real_cudaDeviceGetCount)(int *) = nullptr;
    static cudaError_t (*real_cudaSetDevice)(int) = nullptr;
    static cudaError_t (*real_cudaDeviceCanAccessPeer)(int *, int, int) = nullptr;
    static cudaError_t (*real_cudaDeviceDisablePeerAccess)(int) = nullptr;
    static cudaError_t (*real_cudaDeviceSynchronize)(void) = nullptr;
    static ncclResult_t (*real_ncclCommAbort)(void *) = nullptr;

    if (!real_cudaDeviceGetCount) {
        real_cudaDeviceGetCount = (cudaError_t (*)(int *))
            nvsnap_resolve_real("cudaDeviceGetCount", "libcudart.so");
        real_cudaSetDevice = (cudaError_t (*)(int))
            nvsnap_resolve_real("cudaSetDevice", "libcudart.so");
        real_cudaDeviceCanAccessPeer = (cudaError_t (*)(int *, int, int))
            nvsnap_resolve_real("cudaDeviceCanAccessPeer", "libcudart.so");
        real_cudaDeviceDisablePeerAccess = (cudaError_t (*)(int))
            nvsnap_resolve_real("cudaDeviceDisablePeerAccess", "libcudart.so");
        real_cudaDeviceSynchronize = (cudaError_t (*)(void))
            nvsnap_resolve_real("cudaDeviceSynchronize", "libcudart.so");
        real_ncclCommAbort = (ncclResult_t (*)(void *))
            nvsnap_resolve_real("ncclCommAbort", "libnccl.so.2");
    }

    /* 1. Abort all tracked NCCL communicators.
     * Use ncclCommAbort (immediate, non-blocking), NOT ncclCommDestroy
     * (blocks on pending ops → deadlock when all ranks call simultaneously). */
    if (real_ncclCommAbort) {
        auto &tracker = nvsnap::GpuTracker::instance();
        auto comms = tracker.snapshot_nccl_comms();
        for (auto &c : comms) {
            NVSNAP_GPU_LOG_INFO("quiesce: aborting NCCL comm %p (rank=%d, nranks=%d)",
                              (void *)c.comm, c.rank, c.nranks);
            real_ncclCommAbort((void *)c.comm);
            tracker.untrack_nccl_comm((void *)c.comm);
        }
        NVSNAP_GPU_LOG_INFO("quiesce: aborted %zu NCCL communicators", comms.size());
    }

    /* 2. Disable P2P access between all GPU pairs. */
    if (real_cudaDeviceGetCount && real_cudaSetDevice &&
        real_cudaDeviceCanAccessPeer && real_cudaDeviceDisablePeerAccess) {

        int num_devices = 0;
        real_cudaDeviceGetCount(&num_devices);

        int current_device = nvsnap_get_current_device();
        int p2p_disabled = 0;

        /* Only disable P2P FROM this process's device. Do NOT call
         * cudaSetDevice() for other GPUs — that creates new primary
         * contexts on GPUs this process doesn't own, adding cross-GPU
         * driver state that makes cuCheckpointProcessLock hang. */
        if (current_device >= 0) {
            real_cudaSetDevice(current_device);
            for (int j = 0; j < num_devices; j++) {
                if (j == current_device) continue;
                int can_access = 0;
                real_cudaDeviceCanAccessPeer(&can_access, current_device, j);
                if (can_access) {
                    cudaError_t err = real_cudaDeviceDisablePeerAccess(j);
                    if (err == 0 /* cudaSuccess */) {
                        p2p_disabled++;
                    }
                    /* err=704 (cudaErrorPeerAccessNotEnabled) is fine */
                }
            }
        }

        NVSNAP_GPU_LOG_INFO("quiesce: disabled %d P2P pairs from device %d (%d total devices)",
                          p2p_disabled, current_device, num_devices);
    }

    /* 3. Synchronize all devices. */
    if (real_cudaDeviceSynchronize) {
        real_cudaDeviceSynchronize();
    }

    NVSNAP_GPU_LOG_INFO("quiesce: pre-checkpoint cleanup complete");
    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Post-restore resume: re-enable P2P
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" int nvsnap_post_restore_resume(void)
{
    NVSNAP_GPU_LOG_INFO("resume: re-enabling P2P access (pid=%d)", (int)getpid());

    static cudaError_t (*real_cudaDeviceGetCount)(int *) = nullptr;
    static cudaError_t (*real_cudaSetDevice)(int) = nullptr;
    static cudaError_t (*real_cudaDeviceCanAccessPeer)(int *, int, int) = nullptr;
    static cudaError_t (*real_cudaDeviceEnablePeerAccess)(int, unsigned) = nullptr;

    if (!real_cudaDeviceGetCount) {
        real_cudaDeviceGetCount = (cudaError_t (*)(int *))
            nvsnap_resolve_real("cudaDeviceGetCount", "libcudart.so");
        real_cudaSetDevice = (cudaError_t (*)(int))
            nvsnap_resolve_real("cudaSetDevice", "libcudart.so");
        real_cudaDeviceCanAccessPeer = (cudaError_t (*)(int *, int, int))
            nvsnap_resolve_real("cudaDeviceCanAccessPeer", "libcudart.so");
        real_cudaDeviceEnablePeerAccess = (cudaError_t (*)(int, unsigned))
            nvsnap_resolve_real("cudaDeviceEnablePeerAccess", "libcudart.so");
    }

    if (!real_cudaDeviceGetCount || !real_cudaDeviceEnablePeerAccess)
        return -1;

    int num_devices = 0;
    real_cudaDeviceGetCount(&num_devices);

    int current_device = nvsnap_get_current_device();
    int p2p_enabled = 0;

    /* Only re-enable P2P FROM this process's device. */
    if (current_device >= 0) {
        real_cudaSetDevice(current_device);
        for (int j = 0; j < num_devices; j++) {
            if (j == current_device) continue;
            int can_access = 0;
            real_cudaDeviceCanAccessPeer(&can_access, current_device, j);
            if (can_access) {
                cudaError_t err = real_cudaDeviceEnablePeerAccess(j, 0);
                if (err == 0) p2p_enabled++;
                /* err=704 (already enabled) is fine */
            }
        }
    }

    NVSNAP_GPU_LOG_INFO("resume: enabled %d P2P pairs from device %d", p2p_enabled, current_device);
    return 0;
}

/* ═══════════════════════════════════════════════════════════════════════
 * Query functions (R3)
 * ═══════════════════════════════════════════════════════════════════════ */

extern "C" int nvsnap_get_alloc_count(void)
{
    auto stats = nvsnap::GpuTracker::instance().get_stats();
    return (int)stats.live_alloc_count;
}

extern "C" uint64_t nvsnap_get_total_bytes(void)
{
    auto stats = nvsnap::GpuTracker::instance().get_stats();
    return stats.total_allocated_bytes;
}

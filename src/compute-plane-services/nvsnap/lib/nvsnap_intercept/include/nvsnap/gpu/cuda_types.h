/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Vendored CUDA/NCCL type definitions.
 * Allows compilation without the CUDA toolkit installed.
 * Guarded so these don't conflict if real CUDA headers are also included.
 */
#ifndef NVSNAP_CUDA_TYPES_H
#define NVSNAP_CUDA_TYPES_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ── CUDA Runtime types ──────────────────────────────────────────────── */

#ifndef __DRIVER_TYPES_H__
#ifndef NVSNAP_CUDA_RUNTIME_TYPES_DEFINED
#define NVSNAP_CUDA_RUNTIME_TYPES_DEFINED

typedef enum {
    cudaSuccess                  = 0,
    cudaErrorInvalidValue        = 1,
    cudaErrorMemoryAllocation    = 2,
    cudaErrorInitializationError = 3,
    cudaErrorLaunchFailure       = 719,
    cudaErrorECCUncorrectable    = 72,
    cudaErrorNotReady            = 600,
    cudaErrorInvalidDevice       = 101,
    cudaErrorUnknown             = 999
} cudaError_t;

typedef enum {
    cudaMemcpyHostToHost     = 0,
    cudaMemcpyHostToDevice   = 1,
    cudaMemcpyDeviceToHost   = 2,
    cudaMemcpyDeviceToDevice = 3,
    cudaMemcpyDefault        = 4
} cudaMemcpyKind;

typedef void *cudaStream_t;
typedef void *cudaEvent_t;

typedef struct {
    unsigned int x, y, z;
} dim3;

/* Minimal cudaDeviceProp — enough fields to not crash callers. */
typedef struct {
    char name[256];
    size_t totalGlobalMem;
    size_t sharedMemPerBlock;
    int regsPerBlock;
    int warpSize;
    int maxThreadsPerBlock;
    int maxThreadsDim[3];
    int maxGridSize[3];
    int clockRate;
    int major;
    int minor;
    size_t totalConstMem;
    int multiProcessorCount;
    int l2CacheSize;
    int maxThreadsPerMultiProcessor;
    int computeMode;
    /* Pad to a reasonable size to avoid ABI mismatches with real struct. */
    char _pad[4096 - 256 - 7 * sizeof(size_t)];
} cudaDeviceProp;

/* ── Stream capture mode ──────────────────────────────────────────────── */

typedef enum {
    cudaStreamCaptureModeGlobal      = 0,
    cudaStreamCaptureModeThreadLocal  = 1,
    cudaStreamCaptureModeRelaxed      = 2
} cudaStreamCaptureMode;

typedef enum {
    cudaStreamCaptureStatusNone    = 0,
    cudaStreamCaptureStatusActive  = 1,
    cudaStreamCaptureStatusInvalidated = 2
} cudaStreamCaptureStatus;

/* ── Memory pool types ───────────────────────────────────────────────── */

typedef void *cudaMemPool_t;

typedef enum {
    cudaMemPoolAttrReuseFollowEventDependencies  = 0x1,
    cudaMemPoolAttrReuseAllowOpportunistic       = 0x2,
    cudaMemPoolAttrReuseAllowInternalDependencies = 0x3,
    cudaMemPoolAttrReleaseThreshold              = 0x4,
    cudaMemPoolAttrReservedMemCurrent            = 0x5,
    cudaMemPoolAttrReservedMemHigh               = 0x6,
    cudaMemPoolAttrUsedMemCurrent                = 0x7,
    cudaMemPoolAttrUsedMemHigh                   = 0x8
} cudaMemPoolAttr;

/* Minimal cudaMemPoolProps. */
typedef struct {
    unsigned char _opaque[256];
} cudaMemPoolProps;

/* ── CUDA Array types ────────────────────────────────────────────────── */

typedef void *cudaArray_t;
typedef const void *cudaArray_const_t;
typedef void *cudaMipmappedArray_t;
typedef const void *cudaMipmappedArray_const_t;

/* Minimal channel format desc. */
typedef struct {
    int x, y, z, w;
    int f; /* cudaChannelFormatKind */
} cudaChannelFormatDesc;

/* Minimal cudaExtent. */
typedef struct {
    size_t width, height, depth;
} cudaExtent;

/* Minimal cudaPitchedPtr. */
typedef struct {
    void  *ptr;
    size_t pitch;
    size_t xsize;
    size_t ysize;
} cudaPitchedPtr;

/* Minimal cudaMemcpy3DParms. */
typedef struct {
    unsigned char _opaque[256];
} cudaMemcpy3DParms;

/* Minimal cudaMemcpy3DPeerParms. */
typedef struct {
    unsigned char _opaque[256];
} cudaMemcpy3DPeerParms;

/* Minimal cudaPos. */
typedef struct {
    size_t x, y, z;
} cudaPos;

/* ── Pointer attributes ──────────────────────────────────────────────── */

typedef struct {
    int type; /* cudaMemoryType */
    int device;
    void *devicePointer;
    void *hostPointer;
    int isManaged;
} cudaPointerAttributes;

/* ── Memory range attribute ──────────────────────────────────────────── */

typedef enum {
    cudaMemRangeAttributeReadMostly           = 1,
    cudaMemRangeAttributePreferredLocation    = 2,
    cudaMemRangeAttributeAccessedBy           = 3,
    cudaMemRangeAttributeLastPrefetchLocation = 4
} cudaMemRangeAttribute;

/* ── Memory advise ───────────────────────────────────────────────────── */

typedef enum {
    cudaMemAdviseSetReadMostly          = 1,
    cudaMemAdviseUnsetReadMostly        = 2,
    cudaMemAdviseSetPreferredLocation   = 3,
    cudaMemAdviseUnsetPreferredLocation = 4,
    cudaMemAdviseSetAccessedBy          = 5,
    cudaMemAdviseUnsetAccessedBy        = 6
} cudaMemoryAdvise;

/* ── Device limit enum ───────────────────────────────────────────────── */

typedef enum {
    cudaLimitStackSize                    = 0x00,
    cudaLimitPrintfFifoSize               = 0x01,
    cudaLimitMallocHeapSize               = 0x02,
    cudaLimitDevRuntimeSyncDepth          = 0x03,
    cudaLimitDevRuntimePendingLaunchCount = 0x04,
    cudaLimitMaxL2FetchGranularity        = 0x05
} cudaLimit;

/* ── Cache / shared mem config enums ─────────────────────────────────── */

typedef enum {
    cudaFuncCachePreferNone   = 0,
    cudaFuncCachePreferShared = 1,
    cudaFuncCachePreferL1     = 2,
    cudaFuncCachePreferEqual  = 3
} cudaFuncCache;

typedef enum {
    cudaSharedMemBankSizeDefault   = 0,
    cudaSharedMemBankSizeFourByte  = 1,
    cudaSharedMemBankSizeEightByte = 2
} cudaSharedMemConfig;

/* ── Device P2P attribute ────────────────────────────────────────────── */

typedef enum {
    cudaDevP2PAttrPerformanceRank       = 1,
    cudaDevP2PAttrAccessSupported       = 2,
    cudaDevP2PAttrNativeAtomicSupported = 3,
    cudaDevP2PAttrCudaArrayAccessSupported = 4
} cudaDeviceP2PAttr;

/* ── IPC types ───────────────────────────────────────────────────────── */

typedef struct {
    char reserved[64];
} cudaIpcMemHandle_t;

typedef struct {
    char reserved[64];
} cudaIpcEventHandle_t;

/* ── CUDA Graph types ────────────────────────────────────────────────── */

typedef void *cudaGraph_t;
typedef void *cudaGraphExec_t;
typedef void *cudaGraphNode_t;

typedef enum {
    cudaGraphNodeTypeKernel      = 0,
    cudaGraphNodeTypeMemcpy      = 1,
    cudaGraphNodeTypeMemset      = 2,
    cudaGraphNodeTypeHost        = 3,
    cudaGraphNodeTypeGraph       = 4,
    cudaGraphNodeTypeEmpty       = 5,
    cudaGraphNodeTypeWaitEvent   = 6,
    cudaGraphNodeTypeEventRecord = 7,
    cudaGraphNodeTypeMemAlloc    = 10,
    cudaGraphNodeTypeMemFree     = 11,
    cudaGraphNodeTypeCount       = 12
} cudaGraphNodeType;

typedef enum {
    cudaGraphExecUpdateSuccess                = 0,
    cudaGraphExecUpdateError                  = 1,
    cudaGraphExecUpdateErrorTopologyChanged   = 2,
    cudaGraphExecUpdateErrorNodeTypeChanged   = 3,
    cudaGraphExecUpdateErrorFunctionChanged   = 4,
    cudaGraphExecUpdateErrorParametersChanged = 5,
    cudaGraphExecUpdateErrorNotSupported      = 6,
    cudaGraphExecUpdateErrorUnsupportedFunctionChange = 7
} cudaGraphExecUpdateResult;

/* Minimal graph instantiation params. */
typedef struct {
    unsigned long long flags;
    unsigned char _opaque[64];
} cudaGraphInstantiateParams;

/* ── Kernel node params ──────────────────────────────────────────────── */

typedef struct {
    const void *func;
    dim3 gridDim;
    dim3 blockDim;
    void **kernelParams;
    size_t sharedMemBytes;
    unsigned char _extra[64];
} cudaKernelNodeParams;

typedef struct {
    void *dst;
    size_t pitch;
    int value;
    cudaExtent extent;
    unsigned char _extra[64];
} cudaMemsetParams;

/* ── Host function callback ──────────────────────────────────────────── */

typedef void (*cudaHostFn_t)(void *userData);

/* ── Stream callback ─────────────────────────────────────────────────── */

typedef void (*cudaStreamCallback_t)(cudaStream_t stream, cudaError_t status,
                                     void *userData);

/* ── Texture/Surface types ───────────────────────────────────────────── */

typedef unsigned long long cudaTextureObject_t;
typedef unsigned long long cudaSurfaceObject_t;

/* Minimal texture/surface descriptors. */
typedef struct { unsigned char _opaque[256]; } cudaResourceDesc;
typedef struct { unsigned char _opaque[128]; } cudaTextureDesc;
typedef struct { unsigned char _opaque[128]; } cudaResourceViewDesc;

/* ── Function attributes ─────────────────────────────────────────────── */

typedef struct {
    size_t sharedSizeBytes;
    size_t constSizeBytes;
    size_t localSizeBytes;
    int maxThreadsPerBlock;
    int numRegs;
    int ptxVersion;
    int binaryVersion;
    int cacheModeCA;
    int maxDynamicSharedSizeBytes;
    int preferredShmemCarveout;
} cudaFuncAttributes;

/* ── Function attribute enum ─────────────────────────────────────────── */

typedef enum {
    cudaFuncAttributeMaxDynamicSharedMemorySize = 8,
    cudaFuncAttributePreferredSharedMemoryCarveout = 9,
    cudaFuncAttributeMax = 10
} cudaFuncAttribute;

/* ── External resource types ─────────────────────────────────────────── */

typedef void *cudaExternalMemory_t;
typedef void *cudaExternalSemaphore_t;

typedef struct { unsigned char _opaque[256]; } cudaExternalMemoryHandleDesc;
typedef struct { unsigned char _opaque[128]; } cudaExternalMemoryBufferDesc;
typedef struct { unsigned char _opaque[128]; } cudaExternalMemoryMipmappedArrayDesc;
typedef struct { unsigned char _opaque[256]; } cudaExternalSemaphoreHandleDesc;
typedef struct { unsigned char _opaque[128]; } cudaExternalSemaphoreSignalParams;
typedef struct { unsigned char _opaque[128]; } cudaExternalSemaphoreWaitParams;

/* ── Array sparse properties ─────────────────────────────────────────── */

typedef struct { unsigned char _opaque[64]; } cudaArraySparseProperties;

/* ── Device attribute enum ───────────────────────────────────────────── */

typedef enum {
    cudaDevAttrMaxThreadsPerBlock            = 1,
    cudaDevAttrMaxBlockDimX                  = 2,
    cudaDevAttrMaxBlockDimY                  = 3,
    cudaDevAttrMaxBlockDimZ                  = 4,
    cudaDevAttrMaxGridDimX                   = 5,
    cudaDevAttrMaxGridDimY                   = 6,
    cudaDevAttrMaxGridDimZ                   = 7,
    cudaDevAttrMaxSharedMemoryPerBlock       = 8,
    cudaDevAttrWarpSize                      = 10,
    cudaDevAttrMultiProcessorCount           = 16,
    cudaDevAttrComputeCapabilityMajor        = 75,
    cudaDevAttrComputeCapabilityMinor        = 76
} cudaDeviceAttr;

/* ── Graph dependency add/remove ─────────────────────────────────────── */

typedef enum {
    cudaStreamAddCaptureDependencies    = 0,
    cudaStreamSetCaptureDependencies    = 1
} cudaStreamUpdateCaptureDependenciesFlags;

#endif /* NVSNAP_CUDA_RUNTIME_TYPES_DEFINED */
#endif /* __DRIVER_TYPES_H__ */

/* ── CUDA Driver types ───────────────────────────────────────────────── */

#ifndef CUDA_H_
#ifndef NVSNAP_CUDA_DRIVER_TYPES_DEFINED
#define NVSNAP_CUDA_DRIVER_TYPES_DEFINED

typedef enum {
    CUDA_SUCCESS                    = 0,
    CUDA_ERROR_INVALID_VALUE        = 1,
    CUDA_ERROR_OUT_OF_MEMORY        = 2,
    CUDA_ERROR_NOT_INITIALIZED      = 3,
    CUDA_ERROR_DEINITIALIZED        = 4,
    CUDA_ERROR_INVALID_CONTEXT      = 201,
    CUDA_ERROR_INVALID_HANDLE       = 400,
    CUDA_ERROR_NOT_READY            = 600,
    CUDA_ERROR_ECC_UNCORRECTABLE    = 214,
    CUDA_ERROR_UNKNOWN              = 999
} CUresult;

typedef unsigned long long CUdeviceptr;
typedef int                CUdevice;
typedef void              *CUcontext;
typedef void              *CUfunction;
typedef void              *CUmodule;
typedef void              *CUstream;
typedef void              *CUevent;

/* ── VMM (Virtual Memory Management) types ───────────────────────────── */

typedef unsigned long long CUmemGenericAllocationHandle;

typedef enum {
    CU_MEM_HANDLE_TYPE_NONE          = 0,
    CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR = 1,
    CU_MEM_HANDLE_TYPE_WIN32         = 2,
    CU_MEM_HANDLE_TYPE_WIN32_KMT     = 4
} CUmemAllocationHandleType;

typedef enum {
    CU_MEM_ALLOCATION_TYPE_INVALID = 0,
    CU_MEM_ALLOCATION_TYPE_PINNED  = 1
} CUmemAllocationType;

typedef enum {
    CU_MEM_LOCATION_TYPE_INVALID = 0,
    CU_MEM_LOCATION_TYPE_DEVICE  = 1
} CUmemLocationType;

typedef struct {
    CUmemLocationType type;
    int id;
} CUmemLocation;

typedef struct {
    CUmemAllocationType       type;
    CUmemAllocationHandleType requestedHandleTypes;
    CUmemLocation             location;
    void                     *win32HandleMetaData;
    struct {
        unsigned char compressionType;
        unsigned char gpuDirectRDMACapable;
        unsigned short usage;
        unsigned char reserved[4];
    } allocFlags;
} CUmemAllocationProp;

typedef enum {
    CU_MEM_ACCESS_FLAGS_PROT_NONE      = 0,
    CU_MEM_ACCESS_FLAGS_PROT_READ      = 1,
    CU_MEM_ACCESS_FLAGS_PROT_READWRITE = 3
} CUmemAccess_flags;

typedef struct {
    CUmemLocation      location;
    CUmemAccess_flags  flags;
} CUmemAccessDesc;

typedef enum {
    CU_MEM_ALLOC_GRANULARITY_MINIMUM     = 0,
    CU_MEM_ALLOC_GRANULARITY_RECOMMENDED = 1
} CUmemAllocationGranularity_flags;

/* ── Additional Driver API opaque types ──────────────────────────────── */

typedef void              *CUarray;
typedef void              *CUmipmappedArray;
typedef void              *CUtexref;
typedef void              *CUsurfref;
typedef void              *CUgraph;
typedef void              *CUgraphExec;
typedef void              *CUgraphNode;
typedef void              *CUexternalMemory;
typedef void              *CUexternalSemaphore;
typedef void              *CUmemoryPool;
typedef void              *CUlinkState;

/* ── IPC handles ─────────────────────────────────────────────────────── */

typedef struct { char reserved[64]; } CUipcMemHandle;
typedef struct { char reserved[64]; } CUipcEventHandle;

/* ── UUID ────────────────────────────────────────────────────────────── */

typedef struct { char bytes[16]; } CUuuid;

/* ── Device properties (deprecated struct) ───────────────────────────── */

typedef struct {
    int maxThreadsPerBlock;
    int maxThreadsDim[3];
    int maxGridSize[3];
    int sharedMemPerBlock;
    int totalConstantMemory;
    int SIMDWidth;
    int memPitch;
    int regsPerBlock;
    int clockRate;
    int textureAlign;
} CUdevprop;

/* ── 2D/3D memcpy descriptors ────────────────────────────────────────── */

typedef struct {
    size_t srcXInBytes, srcY;
    CUdeviceptr srcDevice;
    const void *srcHost;
    CUarray srcArray;
    size_t srcPitch;
    size_t dstXInBytes, dstY;
    CUdeviceptr dstDevice;
    void *dstHost;
    CUarray dstArray;
    size_t dstPitch;
    size_t WidthInBytes;
    size_t Height;
} CUDA_MEMCPY2D;

typedef struct {
    size_t srcXInBytes, srcY, srcZ;
    size_t srcLOD;
    CUdeviceptr srcDevice;
    const void *srcHost;
    CUarray srcArray;
    void *reserved0;
    size_t srcPitch;
    size_t srcHeight;
    size_t dstXInBytes, dstY, dstZ;
    size_t dstLOD;
    CUdeviceptr dstDevice;
    void *dstHost;
    CUarray dstArray;
    void *reserved1;
    size_t dstPitch;
    size_t dstHeight;
    size_t WidthInBytes;
    size_t Height;
    size_t Depth;
} CUDA_MEMCPY3D;

typedef struct {
    size_t srcXInBytes, srcY, srcZ;
    size_t srcLOD;
    CUdeviceptr srcDevice;
    const void *srcHost;
    CUarray srcArray;
    CUcontext srcContext;
    size_t srcPitch;
    size_t srcHeight;
    size_t dstXInBytes, dstY, dstZ;
    size_t dstLOD;
    CUdeviceptr dstDevice;
    void *dstHost;
    CUarray dstArray;
    CUcontext dstContext;
    size_t dstPitch;
    size_t dstHeight;
    size_t WidthInBytes;
    size_t Height;
    size_t Depth;
} CUDA_MEMCPY3D_PEER;

/* ── Array descriptors ───────────────────────────────────────────────── */

typedef struct {
    size_t Width;
    size_t Height;
    unsigned int Format;
    unsigned int NumChannels;
} CUDA_ARRAY_DESCRIPTOR;

typedef struct {
    size_t Width;
    size_t Height;
    size_t Depth;
    unsigned int Format;
    unsigned int NumChannels;
    unsigned int Flags;
} CUDA_ARRAY3D_DESCRIPTOR;

/* ── External resource descriptors (opaque) ──────────────────────────── */

typedef struct { char _opaque[256]; } CUDA_EXTERNAL_MEMORY_HANDLE_DESC;
typedef struct { char _opaque[128]; } CUDA_EXTERNAL_MEMORY_BUFFER_DESC;
typedef struct { char _opaque[128]; } CUDA_EXTERNAL_MEMORY_MIPMAPPED_ARRAY_DESC;
typedef struct { char _opaque[256]; } CUDA_EXTERNAL_SEMAPHORE_HANDLE_DESC;
typedef struct { char _opaque[128]; } CUDA_EXTERNAL_SEMAPHORE_SIGNAL_PARAMS;
typedef struct { char _opaque[128]; } CUDA_EXTERNAL_SEMAPHORE_WAIT_PARAMS;

/* ── Graph node params (opaque) ──────────────────────────────────────── */

typedef struct { char _opaque[256]; } CUDA_KERNEL_NODE_PARAMS;
typedef struct { char _opaque[128]; } CUDA_MEMSET_NODE_PARAMS;
typedef struct { char _opaque[64]; }  CUDA_HOST_NODE_PARAMS;
typedef struct { char _opaque[128]; } CUgraphInstantiateParams;
typedef struct { char _opaque[128]; } CUDA_MEM_ALLOC_NODE_PARAMS;

/* ── Memory pool ─────────────────────────────────────────────────────── */

typedef struct { char _opaque[64]; } CUmemPoolProps;

/* ── Context creation v3 exec affinity ───────────────────────────────── */

typedef struct { char _opaque[16]; } CUexecAffinityParam;

/* ── Enums used as int in the driver API ─────────────────────────────── */

typedef int CUdevice_attribute;
typedef int CUlimit;
typedef int CUfunc_cache;
typedef int CUsharedconfig;
typedef int CUmem_advise;
typedef int CUmem_range_attribute;

/* ── Pointer attribute enum ──────────────────────────────────────────── */

typedef enum {
    CU_POINTER_ATTRIBUTE_CONTEXT = 1,
    CU_POINTER_ATTRIBUTE_MEMORY_TYPE = 2,
    CU_POINTER_ATTRIBUTE_DEVICE_POINTER = 3,
    CU_POINTER_ATTRIBUTE_HOST_POINTER = 4,
    CU_POINTER_ATTRIBUTE_P2P_TOKENS = 5,
    CU_POINTER_ATTRIBUTE_SYNC_MEMOPS = 6,
    CU_POINTER_ATTRIBUTE_BUFFER_ID = 7,
    CU_POINTER_ATTRIBUTE_IS_MANAGED = 8,
    CU_POINTER_ATTRIBUTE_DEVICE_ORDINAL = 9
} CUpointer_attribute;

/* ── P2P attribute enum ──────────────────────────────────────────────── */

typedef enum {
    CU_DEVICE_P2P_ATTRIBUTE_PERFORMANCE_RANK = 0x01,
    CU_DEVICE_P2P_ATTRIBUTE_ACCESS_SUPPORTED = 0x02,
    CU_DEVICE_P2P_ATTRIBUTE_NATIVE_ATOMIC_SUPPORTED = 0x03,
    CU_DEVICE_P2P_ATTRIBUTE_CUDA_ARRAY_ACCESS_SUPPORTED = 0x04
} CUdevice_P2PAttribute;

/* ── Stream capture ──────────────────────────────────────────────────── */

typedef enum {
    CU_STREAM_CAPTURE_MODE_GLOBAL = 0,
    CU_STREAM_CAPTURE_MODE_THREAD_LOCAL = 1,
    CU_STREAM_CAPTURE_MODE_RELAXED = 2
} CUstreamCaptureMode;

typedef enum {
    CU_STREAM_CAPTURE_STATUS_NONE = 0,
    CU_STREAM_CAPTURE_STATUS_ACTIVE = 1,
    CU_STREAM_CAPTURE_STATUS_INVALIDATED = 2
} CUstreamCaptureStatus;

/* ── Graph exec update result ────────────────────────────────────────── */

typedef enum {
    CU_GRAPH_EXEC_UPDATE_SUCCESS = 0x0,
    CU_GRAPH_EXEC_UPDATE_ERROR = 0x1,
    CU_GRAPH_EXEC_UPDATE_ERROR_TOPOLOGY_CHANGED = 0x2,
    CU_GRAPH_EXEC_UPDATE_ERROR_NODE_TYPE_CHANGED = 0x3,
    CU_GRAPH_EXEC_UPDATE_ERROR_FUNCTION_CHANGED = 0x4,
    CU_GRAPH_EXEC_UPDATE_ERROR_PARAMETERS_CHANGED = 0x5,
    CU_GRAPH_EXEC_UPDATE_ERROR_NOT_SUPPORTED = 0x6
} CUgraphExecUpdateResult;

/* ── Stream callback / host function ─────────────────────────────────── */

typedef void (*CUstreamCallback)(CUstream stream, CUresult status, void *userData);
typedef void (*CUhostFn)(void *userData);

/* ── Occupancy callback ──────────────────────────────────────────────── */

typedef size_t (*CUoccupancyB2DSize)(int blockSize);

/* ── Function attribute enum ─────────────────────────────────────────── */

typedef int CUfunction_attribute;

#endif /* NVSNAP_CUDA_DRIVER_TYPES_DEFINED */
#endif /* CUDA_H_ */

/* ── NCCL types ──────────────────────────────────────────────────────── */

#ifndef NCCL_H_
#ifndef NVSNAP_NCCL_TYPES_DEFINED
#define NVSNAP_NCCL_TYPES_DEFINED

typedef enum {
    ncclSuccess               = 0,
    ncclUnhandledCudaError    = 1,
    ncclSystemError           = 2,
    ncclInternalError         = 3,
    ncclInvalidArgument       = 4,
    ncclInvalidUsage          = 5,
    ncclNumResults            = 6
} ncclResult_t;

typedef void *ncclComm_t;

typedef struct {
    char internal[128];
} ncclUniqueId;

typedef enum {
    ncclInt8    = 0,
    ncclChar    = 0,
    ncclUint8   = 1,
    ncclInt32   = 2,
    ncclInt     = 2,
    ncclUint32  = 3,
    ncclInt64   = 4,
    ncclUint64  = 5,
    ncclFloat16 = 6,
    ncclHalf    = 6,
    ncclFloat32 = 7,
    ncclFloat   = 7,
    ncclFloat64 = 8,
    ncclDouble  = 8,
    ncclBfloat16 = 9,
    ncclNumTypes = 10
} ncclDataType_t;

typedef enum {
    ncclSum  = 0,
    ncclProd = 1,
    ncclMax  = 2,
    ncclMin  = 3,
    ncclAvg  = 4,
    ncclNumOps = 5
} ncclRedOp_t;

#endif /* NVSNAP_NCCL_TYPES_DEFINED */
#endif /* NCCL_H_ */

#ifdef __cplusplus
}
#endif

#endif /* NVSNAP_CUDA_TYPES_H */

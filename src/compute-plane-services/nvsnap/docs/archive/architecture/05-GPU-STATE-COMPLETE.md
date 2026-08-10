# Complete GPU State Catalog

## The Problem Statement

To checkpoint a GPU application, we must capture **ALL** state that affects program behavior. Miss anything, and restore fails or produces wrong results.

## Complete State Categories

### 1. CUDA Driver API State

#### 1.1 Contexts
```c
// What we intercept
CUresult cuCtxCreate(CUcontext *pctx, unsigned int flags, CUdevice dev);
CUresult cuCtxDestroy(CUcontext ctx);
CUresult cuCtxPushCurrent(CUcontext ctx);
CUresult cuCtxPopCurrent(CUcontext *pctx);
CUresult cuCtxSetCurrent(CUcontext ctx);
CUresult cuCtxGetCurrent(CUcontext *pctx);
CUresult cuDevicePrimaryCtxRetain(CUcontext *pctx, CUdevice dev);
CUresult cuDevicePrimaryCtxRelease(CUdevice dev);

// State to track
struct ContextState {
    CUcontext handle;           // Original handle (for remapping)
    CUdevice device;            // Which GPU
    unsigned int flags;         // Creation flags
    bool isPrimary;             // Primary context vs explicit
    int refCount;               // Reference count
    // Context stack per thread
};
```

#### 1.2 Device Memory Allocations
```c
// What we intercept
CUresult cuMemAlloc(CUdeviceptr *dptr, size_t bytesize);
CUresult cuMemAllocPitch(CUdeviceptr *dptr, size_t *pPitch, size_t WidthInBytes, size_t Height, unsigned int ElementSizeBytes);
CUresult cuMemAllocManaged(CUdeviceptr *dptr, size_t bytesize, unsigned int flags);
CUresult cuMemFree(CUdeviceptr dptr);
CUresult cuMemcpy(CUdeviceptr dst, CUdeviceptr src, size_t ByteCount);
CUresult cuMemcpyAsync(CUdeviceptr dst, CUdeviceptr src, size_t ByteCount, CUstream hStream);

// State to track
struct MemoryAllocation {
    CUdeviceptr device_ptr;     // GPU address
    size_t size;                // Allocation size
    unsigned int flags;         // CU_MEM_ATTACH_* flags
    AllocationType type;        // DEVICE, MANAGED, PINNED
    CUcontext context;          // Owning context
    
    // For pitched allocations
    size_t pitch;
    size_t width, height;
    
    // For checkpoint
    void* host_shadow;          // Host copy of data (lazy, on checkpoint)
};
```

#### 1.3 Host Pinned Memory
```c
// What we intercept
CUresult cuMemHostAlloc(void **pp, size_t bytesize, unsigned int Flags);
CUresult cuMemAllocHost(void **pp, size_t bytesize);
CUresult cuMemFreeHost(void *p);
CUresult cuMemHostRegister(void *p, size_t bytesize, unsigned int Flags);
CUresult cuMemHostUnregister(void *p);

// State to track
struct PinnedHostAllocation {
    void* host_ptr;
    size_t size;
    unsigned int flags;         // CU_MEMHOSTALLOC_* flags
    bool isRegistered;          // cuMemHostRegister vs cuMemHostAlloc
    CUdeviceptr device_ptr;     // If CU_MEMHOSTALLOC_DEVICEMAP
};
```

#### 1.4 Streams
```c
// What we intercept
CUresult cuStreamCreate(CUstream *phStream, unsigned int Flags);
CUresult cuStreamCreateWithPriority(CUstream *phStream, unsigned int flags, int priority);
CUresult cuStreamDestroy(CUstream hStream);
CUresult cuStreamSynchronize(CUstream hStream);
CUresult cuStreamWaitEvent(CUstream hStream, CUevent hEvent, unsigned int Flags);

// State to track
struct StreamState {
    CUstream handle;
    unsigned int flags;
    int priority;
    CUcontext context;
    
    // For restore: recreate with same properties
    // Note: in-flight work is NOT preserved - must sync first
};
```

#### 1.5 Events
```c
// What we intercept
CUresult cuEventCreate(CUevent *phEvent, unsigned int Flags);
CUresult cuEventDestroy(CUevent hEvent);
CUresult cuEventRecord(CUevent hEvent, CUstream hStream);
CUresult cuEventSynchronize(CUevent hEvent);
CUresult cuEventQuery(CUevent hEvent);

// State to track
struct EventState {
    CUevent handle;
    unsigned int flags;         // CU_EVENT_* flags
    CUcontext context;
    bool isRecorded;
    CUstream recordedOnStream;
};
```

#### 1.6 Modules and Functions (Kernels)
```c
// What we intercept
CUresult cuModuleLoad(CUmodule *module, const char *fname);
CUresult cuModuleLoadData(CUmodule *module, const void *image);
CUresult cuModuleLoadDataEx(CUmodule *module, const void *image, unsigned int numOptions, CUjit_option *options, void **optionValues);
CUresult cuModuleUnload(CUmodule hmod);
CUresult cuModuleGetFunction(CUfunction *hfunc, CUmodule hmod, const char *name);

// State to track
struct ModuleState {
    CUmodule handle;
    std::vector<uint8_t> image;  // PTX or cubin data
    std::string filename;        // If loaded from file
    CUcontext context;
    
    // JIT options
    std::vector<CUjit_option> options;
    std::vector<void*> optionValues;
    
    // Functions within module
    std::unordered_map<std::string, CUfunction> functions;
};
```

#### 1.7 Textures and Surfaces
```c
// What we intercept  
CUresult cuTexObjectCreate(CUtexObject *pTexObject, const CUDA_RESOURCE_DESC *pResDesc, const CUDA_TEXTURE_DESC *pTexDesc, const CUDA_RESOURCE_VIEW_DESC *pResViewDesc);
CUresult cuTexObjectDestroy(CUtexObject texObject);
CUresult cuSurfObjectCreate(CUsurfObject *pSurfObject, const CUDA_RESOURCE_DESC *pResDesc);
CUresult cuSurfObjectDestroy(CUsurfObject surfObject);

// Legacy texture references
CUresult cuTexRefSetAddress(size_t *ByteOffset, CUtexref hTexRef, CUdeviceptr dptr, size_t bytes);
CUresult cuTexRefSetArray(CUtexref hTexRef, CUarray hArray, unsigned int Flags);

// State to track
struct TextureState {
    CUtexObject handle;
    CUDA_RESOURCE_DESC resDesc;
    CUDA_TEXTURE_DESC texDesc;
    CUDA_RESOURCE_VIEW_DESC viewDesc;
    CUcontext context;
};
```

#### 1.8 Arrays (for textures/surfaces)
```c
// What we intercept
CUresult cuArrayCreate(CUarray *pHandle, const CUDA_ARRAY_DESCRIPTOR *pAllocateArray);
CUresult cuArray3DCreate(CUarray *pHandle, const CUDA_ARRAY3D_DESCRIPTOR *pAllocateArray);
CUresult cuArrayDestroy(CUarray hArray);
CUresult cuMipmappedArrayCreate(CUmipmappedArray *pHandle, const CUDA_ARRAY3D_DESCRIPTOR *pMipmappedArrayDesc, unsigned int numMipmapLevels);

// State to track
struct ArrayState {
    CUarray handle;
    CUDA_ARRAY_DESCRIPTOR desc;      // or CUDA_ARRAY3D_DESCRIPTOR
    bool is3D;
    unsigned int mipmapLevels;
    CUcontext context;
    
    // Data content - more complex than linear memory
    std::vector<uint8_t> data;
};
```

#### 1.9 CUDA Graphs
```c
// What we intercept
CUresult cuGraphCreate(CUgraph *phGraph, unsigned int flags);
CUresult cuGraphDestroy(CUgraph hGraph);
CUresult cuGraphInstantiate(CUgraphExec *phGraphExec, CUgraph hGraph, ...);
CUresult cuGraphExecDestroy(CUgraphExec hGraphExec);
CUresult cuGraphLaunch(CUgraphExec hGraphExec, CUstream hStream);

// Graph capture
CUresult cuStreamBeginCapture(CUstream hStream, CUstreamCaptureMode mode);
CUresult cuStreamEndCapture(CUstream hStream, CUgraph *phGraph);

// State to track
struct GraphState {
    CUgraph handle;
    unsigned int flags;
    CUcontext context;
    
    // Graph structure - nodes and edges
    std::vector<GraphNode> nodes;
    std::vector<GraphEdge> edges;
    
    // Instantiated executables
    std::vector<CUgraphExec> executables;
};
```

### 2. CUDA Runtime API State

The runtime API is a higher-level wrapper. We intercept both for completeness.

```c
// Memory
cudaError_t cudaMalloc(void **devPtr, size_t size);
cudaError_t cudaMallocManaged(void **devPtr, size_t size, unsigned int flags);
cudaError_t cudaMallocHost(void **ptr, size_t size);
cudaError_t cudaHostAlloc(void **pHost, size_t size, unsigned int flags);
cudaError_t cudaFree(void *devPtr);
cudaError_t cudaFreeHost(void *ptr);

// Streams
cudaError_t cudaStreamCreate(cudaStream_t *pStream);
cudaError_t cudaStreamCreateWithFlags(cudaStream_t *pStream, unsigned int flags);
cudaError_t cudaStreamCreateWithPriority(cudaStream_t *pStream, unsigned int flags, int priority);
cudaError_t cudaStreamDestroy(cudaStream_t stream);

// Events
cudaError_t cudaEventCreate(cudaEvent_t *event);
cudaError_t cudaEventCreateWithFlags(cudaEvent_t *event, unsigned int flags);
cudaError_t cudaEventDestroy(cudaEvent_t event);

// Device management
cudaError_t cudaSetDevice(int device);
cudaError_t cudaDeviceReset();
```

### 3. cuBLAS State

```c
// What we intercept
cublasStatus_t cublasCreate(cublasHandle_t *handle);
cublasStatus_t cublasDestroy(cublasHandle_t handle);
cublasStatus_t cublasSetStream(cublasHandle_t handle, cudaStream_t streamId);
cublasStatus_t cublasSetMathMode(cublasHandle_t handle, cublasMath_t mode);
cublasStatus_t cublasSetPointerMode(cublasHandle_t handle, cublasPointerMode_t mode);

// Workspace
cublasStatus_t cublasSetWorkspace(cublasHandle_t handle, void *workspace, size_t workspaceSizeInBytes);

// State to track
struct CublasState {
    cublasHandle_t handle;
    cudaStream_t stream;
    cublasMath_t mathMode;
    cublasPointerMode_t pointerMode;
    void* workspace;
    size_t workspaceSize;
    
    // Internal state we can't directly query
    // Must track from creation
};
```

### 4. cuDNN State

```c
// What we intercept
cudnnStatus_t cudnnCreate(cudnnHandle_t *handle);
cudnnStatus_t cudnnDestroy(cudnnHandle_t handle);
cudnnStatus_t cudnnSetStream(cudnnHandle_t handle, cudaStream_t streamId);

// Descriptors
cudnnStatus_t cudnnCreateTensorDescriptor(cudnnTensorDescriptor_t *tensorDesc);
cudnnStatus_t cudnnSetTensor4dDescriptor(cudnnTensorDescriptor_t tensorDesc, ...);
cudnnStatus_t cudnnDestroyTensorDescriptor(cudnnTensorDescriptor_t tensorDesc);

cudnnStatus_t cudnnCreateFilterDescriptor(cudnnFilterDescriptor_t *filterDesc);
cudnnStatus_t cudnnCreateConvolutionDescriptor(cudnnConvolutionDescriptor_t *convDesc);
cudnnStatus_t cudnnCreatePoolingDescriptor(cudnnPoolingDescriptor_t *poolingDesc);
cudnnStatus_t cudnnCreateActivationDescriptor(cudnnActivationDescriptor_t *activationDesc);
cudnnStatus_t cudnnCreateDropoutDescriptor(cudnnDropoutDescriptor_t *dropoutDesc);
cudnnStatus_t cudnnCreateRNNDescriptor(cudnnRNNDescriptor_t *rnnDesc);
cudnnStatus_t cudnnCreateAttnDescriptor(cudnnAttnDescriptor_t *attnDesc);

// State to track
struct CudnnState {
    cudnnHandle_t handle;
    cudaStream_t stream;
    
    // All descriptor states
    std::vector<TensorDescState> tensorDescs;
    std::vector<FilterDescState> filterDescs;
    std::vector<ConvDescState> convDescs;
    // ... many more descriptor types
    
    // Dropout states (have RNG state!)
    std::vector<DropoutState> dropoutStates;
};

struct DropoutState {
    cudnnDropoutDescriptor_t desc;
    float dropout;
    void* states;           // GPU memory with RNG state
    size_t stateSizeInBytes;
    unsigned long long seed;
};
```

### 5. cuFFT State

```c
// What we intercept
cufftResult cufftCreate(cufftHandle *plan);
cufftResult cufftDestroy(cufftHandle plan);
cufftResult cufftPlan1d(cufftHandle *plan, int nx, cufftType type, int batch);
cufftResult cufftPlan2d(cufftHandle *plan, int nx, int ny, cufftType type);
cufftResult cufftPlan3d(cufftHandle *plan, int nx, int ny, int nz, cufftType type);
cufftResult cufftPlanMany(cufftHandle *plan, int rank, int *n, ...);
cufftResult cufftSetStream(cufftHandle plan, cudaStream_t stream);
cufftResult cufftSetWorkArea(cufftHandle plan, void *workArea);

// State to track
struct CufftState {
    cufftHandle handle;
    cufftType type;
    int rank;
    std::vector<int> dimensions;
    int batch;
    cudaStream_t stream;
    void* workArea;
    size_t workSize;
};
```

### 6. cuRAND State

```c
// What we intercept
curandStatus_t curandCreateGenerator(curandGenerator_t *generator, curandRngType_t rng_type);
curandStatus_t curandDestroyGenerator(curandGenerator_t generator);
curandStatus_t curandSetPseudoRandomGeneratorSeed(curandGenerator_t generator, unsigned long long seed);
curandStatus_t curandSetGeneratorOffset(curandGenerator_t generator, unsigned long long offset);
curandStatus_t curandSetStream(curandGenerator_t generator, cudaStream_t stream);

// State to track
struct CurandState {
    curandGenerator_t handle;
    curandRngType_t type;
    unsigned long long seed;
    unsigned long long offset;      // CRITICAL: current position in sequence
    cudaStream_t stream;
    
    // For reproducibility, we MUST track offset
    // Random sequence continues from exactly where it was
};
```

### 7. NCCL State

```c
// What we intercept
ncclResult_t ncclCommInitRank(ncclComm_t* comm, int nranks, ncclUniqueId commId, int rank);
ncclResult_t ncclCommInitAll(ncclComm_t* comms, int ndev, const int* devlist);
ncclResult_t ncclCommDestroy(ncclComm_t comm);
ncclResult_t ncclCommAbort(ncclComm_t comm);

// Get info for reconstruction
ncclResult_t ncclCommCount(const ncclComm_t comm, int* count);
ncclResult_t ncclCommUserRank(const ncclComm_t comm, int* rank);
ncclResult_t ncclGetUniqueId(ncclUniqueId* uniqueId);

// Collective operations (for tracking in-flight)
ncclResult_t ncclAllReduce(const void* sendbuff, void* recvbuff, size_t count, ncclDataType_t datatype, ncclRedOp_t op, ncclComm_t comm, cudaStream_t stream);
ncclResult_t ncclBroadcast(const void* sendbuff, void* recvbuff, size_t count, ncclDataType_t datatype, int root, ncclComm_t comm, cudaStream_t stream);
ncclResult_t ncclReduce(const void* sendbuff, void* recvbuff, size_t count, ncclDataType_t datatype, ncclRedOp_t op, int root, ncclComm_t comm, cudaStream_t stream);
ncclResult_t ncclAllGather(const void* sendbuff, void* recvbuff, size_t sendcount, ncclDataType_t datatype, ncclComm_t comm, cudaStream_t stream);
ncclResult_t ncclReduceScatter(const void* sendbuff, void* recvbuff, size_t recvcount, ncclDataType_t datatype, ncclRedOp_t op, ncclComm_t comm, cudaStream_t stream);

ncclResult_t ncclGroupStart();
ncclResult_t ncclGroupEnd();

// State to track
struct NcclCommState {
    ncclComm_t handle;
    int nranks;
    int rank;
    ncclUniqueId uniqueId;      // For reconstruction coordination
    CUdevice device;
    
    // CANNOT serialize the actual comm - must recreate
    // On restore: generate new uniqueId, all ranks ncclCommInitRank
};
```

### 8. IPC (Inter-Process Communication) State

```c
// What we intercept
CUresult cuIpcGetMemHandle(CUipcMemHandle *pHandle, CUdeviceptr dptr);
CUresult cuIpcOpenMemHandle(CUdeviceptr *pdptr, CUipcMemHandle handle, unsigned int Flags);
CUresult cuIpcCloseMemHandle(CUdeviceptr dptr);

CUresult cuIpcGetEventHandle(CUipcEventHandle *pHandle, CUevent event);
CUresult cuIpcOpenEventHandle(CUevent *phEvent, CUipcEventHandle handle);

// State to track
struct IpcMemoryState {
    CUipcMemHandle handle;
    CUdeviceptr localPtr;
    size_t size;
    bool isExported;            // We exported (own the memory)
    bool isImported;            // We imported from another process
    pid_t ownerPid;             // Process that owns the memory
    
    // For restore: coordinate with owner process
    // Owner exports new handle, importers receive and open
};
```

### 9. Virtual Memory Management (CUDA 10.2+)

```c
// What we intercept
CUresult cuMemCreate(CUmemGenericAllocationHandle *handle, size_t size, const CUmemAllocationProp *prop, unsigned long long flags);
CUresult cuMemRelease(CUmemGenericAllocationHandle handle);
CUresult cuMemMap(CUdeviceptr ptr, size_t size, size_t offset, CUmemGenericAllocationHandle handle, unsigned long long flags);
CUresult cuMemUnmap(CUdeviceptr ptr, size_t size);
CUresult cuMemAddressReserve(CUdeviceptr *ptr, size_t size, size_t alignment, CUdeviceptr addr, unsigned long long flags);
CUresult cuMemAddressFree(CUdeviceptr ptr, size_t size);
CUresult cuMemSetAccess(CUdeviceptr ptr, size_t size, const CUmemAccessDesc *desc, size_t count);

// State to track
struct VirtualMemoryState {
    CUdeviceptr reservedAddr;
    size_t reservedSize;
    
    struct Mapping {
        CUdeviceptr addr;
        size_t size;
        size_t offset;
        CUmemGenericAllocationHandle handle;
    };
    std::vector<Mapping> mappings;
    
    struct AllocationHandle {
        CUmemGenericAllocationHandle handle;
        size_t size;
        CUmemAllocationProp prop;
    };
    std::vector<AllocationHandle> handles;
};
```

### 10. External Resources (Vulkan/OpenGL Interop)

```c
// What we intercept
CUresult cuImportExternalMemory(CUexternalMemory *extMem_out, const CUDA_EXTERNAL_MEMORY_HANDLE_DESC *memHandleDesc);
CUresult cuDestroyExternalMemory(CUexternalMemory extMem);
CUresult cuExternalMemoryGetMappedBuffer(CUdeviceptr *devPtr, CUexternalMemory extMem, const CUDA_EXTERNAL_MEMORY_BUFFER_DESC *bufferDesc);
CUresult cuExternalMemoryGetMappedMipmappedArray(CUmipmappedArray *mipmap, CUexternalMemory extMem, const CUDA_EXTERNAL_MEMORY_MIPMAPPED_ARRAY_DESC *mipmapDesc);

CUresult cuImportExternalSemaphore(CUexternalSemaphore *extSem_out, const CUDA_EXTERNAL_SEMAPHORE_HANDLE_DESC *semHandleDesc);
CUresult cuDestroyExternalSemaphore(CUexternalSemaphore extSem);
CUresult cuSignalExternalSemaphoresAsync(const CUexternalSemaphore *extSemArray, const CUDA_EXTERNAL_SEMAPHORE_SIGNAL_PARAMS *paramsArray, unsigned int numExtSems, CUstream stream);
CUresult cuWaitExternalSemaphoresAsync(const CUexternalSemaphore *extSemArray, const CUDA_EXTERNAL_SEMAPHORE_WAIT_PARAMS *paramsArray, unsigned int numExtSems, CUstream stream);

// State to track - VERY COMPLEX
// External resources are owned by Vulkan/OpenGL
// We'd need to coordinate with those APIs too
// LIMITATION: May not support graphics interop initially
```

## The Handle Remapping Problem

On restore, all handles will be different:

```
CHECKPOINT                          RESTORE
───────────────────────────────────────────────────────────
cudaMalloc → 0x7f1234560000        cudaMalloc → 0x7f9876540000
cuStreamCreate → 0x5555555500a0    cuStreamCreate → 0x5555666600b0
ncclCommInit → 0x1234              ncclCommInit → 0x5678

Application still holds old pointers!
```

### Solution: Shadow Handle Table

```c
// lib/nvsnap/handle_remap.c

typedef struct {
    void* old_handle;
    void* new_handle;
} HandleMapping;

typedef struct {
    HandleMapping* mappings;
    size_t count;
    size_t capacity;
    pthread_rwlock_t lock;
} HandleTable;

// Global tables per handle type
static HandleTable g_mem_handles;
static HandleTable g_stream_handles;
static HandleTable g_event_handles;
static HandleTable g_nccl_handles;

// On every CUDA call, remap handles
CUresult cuMemcpyDtoH(void *dstHost, CUdeviceptr srcDevice, size_t ByteCount) {
    // Remap the device pointer if it's from before checkpoint
    CUdeviceptr remapped = remap_device_ptr(srcDevice);
    
    return real_cuMemcpyDtoH(dstHost, remapped, ByteCount);
}

CUdeviceptr remap_device_ptr(CUdeviceptr ptr) {
    pthread_rwlock_rdlock(&g_mem_handles.lock);
    
    for (size_t i = 0; i < g_mem_handles.count; i++) {
        HandleMapping* m = &g_mem_handles.mappings[i];
        // Check if ptr falls within old allocation range
        if (ptr >= (CUdeviceptr)m->old_handle && 
            ptr < (CUdeviceptr)m->old_handle + m->size) {
            size_t offset = ptr - (CUdeviceptr)m->old_handle;
            pthread_rwlock_unlock(&g_mem_handles.lock);
            return (CUdeviceptr)m->new_handle + offset;
        }
    }
    
    pthread_rwlock_unlock(&g_mem_handles.lock);
    return ptr;  // Not remapped, use as-is
}
```

### Alternative: Reserved Virtual Address Space

```c
// Reserve the SAME virtual address on restore
// This way, pointers don't need remapping

CUresult restore_memory_allocation(MemoryAllocation* alloc) {
    CUdeviceptr new_ptr;
    
    // Try to allocate at the exact same address
    CUresult result = cuMemAddressReserve(&new_ptr, alloc->size, 0, 
                                          alloc->device_ptr,  // Hint: same addr
                                          0);
    
    if (result == CUDA_SUCCESS && new_ptr == alloc->device_ptr) {
        // Got the same address! No remapping needed
        // Now create backing allocation and map
        CUmemGenericAllocationHandle handle;
        CUmemAllocationProp prop = { .type = CU_MEM_ALLOCATION_TYPE_PINNED };
        cuMemCreate(&handle, alloc->size, &prop, 0);
        cuMemMap(new_ptr, alloc->size, 0, handle, 0);
        
        // Restore data
        cuMemcpyHtoD(new_ptr, alloc->host_shadow, alloc->size);
        
        return CUDA_SUCCESS;
    }
    
    // Couldn't get same address, need remapping
    return restore_with_remapping(alloc);
}
```

## Complete Function List to Intercept

### Critical (Must Have for Basic Function)

| Category | Functions | Count |
|----------|-----------|-------|
| Memory | cuMemAlloc, cuMemFree, cuMemcpy*, cudaMalloc, cudaFree | ~20 |
| Streams | cuStreamCreate/Destroy, cudaStreamCreate/Destroy | ~10 |
| Events | cuEventCreate/Destroy, cudaEventCreate/Destroy | ~8 |
| Context | cuCtxCreate/Destroy/SetCurrent | ~12 |
| Modules | cuModuleLoad*, cuModuleUnload, cuModuleGetFunction | ~8 |
| Sync | cuStreamSynchronize, cudaDeviceSynchronize | ~6 |

### Important (For Real Workloads)

| Category | Functions | Count |
|----------|-----------|-------|
| cuBLAS | cublasCreate/Destroy, all BLAS operations | ~100+ |
| cuDNN | cudnnCreate, all convolution/pooling/activation | ~200+ |
| cuFFT | cufftCreate, cufftPlan*, cufftExec* | ~30 |
| cuRAND | curandCreate, curandGenerate* | ~20 |
| NCCL | ncclCommInit*, ncclAllReduce, ncclBroadcast | ~25 |

### Advanced (For Complex Workloads)

| Category | Functions | Count |
|----------|-----------|-------|
| Graphs | cuGraphCreate, cuStreamBeginCapture | ~30 |
| IPC | cuIpcGet/OpenMemHandle | ~6 |
| VMM | cuMemCreate, cuMemMap, cuMemAddressReserve | ~15 |
| Textures | cuTexObjectCreate, cuArrayCreate | ~40 |
| External | cuImportExternalMemory/Semaphore | ~10 |

**Total: 500+ functions for comprehensive coverage**

## Checkpoint Procedure

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        CHECKPOINT PROCEDURE                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. QUIESCE                                                                 │
│     ├── Signal application to pause (or freeze cgroup)                     │
│     ├── For NCCL: Wait for ncclGroupEnd(), don't start new ops             │
│     └── Call cudaDeviceSynchronize() on all contexts                       │
│                                                                             │
│  2. CAPTURE GPU MEMORY                                                      │
│     ├── For each allocation in our tracking table:                         │
│     │   ├── Allocate host buffer (if not already)                          │
│     │   ├── cuMemcpyDtoH(host_buffer, device_ptr, size)                    │
│     │   └── Optionally compress                                            │
│     └── Note: Use async copies + streams for parallelism                   │
│                                                                             │
│  3. SERIALIZE HANDLE TABLES                                                 │
│     ├── All contexts (device, flags)                                       │
│     ├── All allocations (ptr, size, type)                                  │
│     ├── All streams (flags, priority)                                      │
│     ├── All events (flags)                                                 │
│     ├── All modules (PTX/cubin data)                                       │
│     ├── All library handles (cuBLAS, cuDNN, etc.)                          │
│     └── All NCCL comms (nranks, rank, uniqueId)                            │
│                                                                             │
│  4. CRIU CHECKPOINT                                                         │
│     └── Standard CRIU for CPU state, files, etc.                           │
│                                                                             │
│  5. WRITE TO STORAGE                                                        │
│     ├── GPU memory dumps (potentially large)                               │
│     ├── Handle tables (small, JSON or protobuf)                            │
│     └── CRIU images                                                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Restore Procedure

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         RESTORE PROCEDURE                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. LOAD DATA FROM STORAGE                                                  │
│     ├── Read handle tables                                                  │
│     ├── Read GPU memory dumps                                               │
│     └── Read CRIU images                                                    │
│                                                                             │
│  2. RECREATE GPU STATE (before CRIU restore)                                │
│     ├── Initialize CUDA driver                                              │
│     ├── Create contexts on appropriate devices                              │
│     ├── Load modules (cuModuleLoadData)                                     │
│     ├── Allocate memory (try same addresses)                                │
│     │   └── Build old→new handle mapping table                             │
│     ├── Create streams with same properties                                 │
│     ├── Create events with same flags                                       │
│     ├── Copy memory contents (cuMemcpyHtoD)                                 │
│     └── Recreate library handles (cuBLAS, cuDNN, etc.)                     │
│                                                                             │
│  3. NCCL RECONSTRUCTION (multi-process coordination)                        │
│     ├── Rank 0: Generate new ncclUniqueId                                  │
│     ├── Broadcast uniqueId to all ranks                                    │
│     ├── All ranks: ncclCommInitRank(nranks, uniqueId, rank)                │
│     └── Add to handle remap table                                          │
│                                                                             │
│  4. INSTALL HANDLE REMAPPING                                                │
│     └── Future CUDA calls go through remap layer                           │
│                                                                             │
│  5. CRIU RESTORE                                                            │
│     └── Resume CPU state, application continues                            │
│                                                                             │
│  6. APPLICATION RUNS                                                        │
│     └── Uses old handles, we transparently remap                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Known Limitations and Challenges

### 1. Library Version Compatibility

```
Problem: Application checkpointed with cuBLAS 11.0, restore on cuBLAS 12.0
         Internal handle layouts may differ

Solution: 
- Track library versions in checkpoint
- Validate compatible versions on restore
- Recreate handles (don't serialize internal state)
```

### 2. Driver Version Compatibility

```
Problem: NVIDIA driver 525 → 535 may change internal structures

Solution:
- Recreate all handles from parameters
- Don't depend on internal driver state
- Test across driver versions in CI
```

### 3. GPU Generation Differences

```
Problem: Checkpoint on A100, restore on H100
         Memory layout, compute capabilities differ

Solution:
- Check compute capability compatibility
- Recompile PTX for target architecture
- May need to re-JIT kernels
```

### 4. Multi-Process Atomicity

```
Problem: 4 processes with NCCL, must checkpoint atomically

Solution:
- Barrier protocol across processes
- Coordinator signals checkpoint start
- All processes quiesce, sync, then dump
- CRIU checkpoints all together
```

### 5. In-Flight Operations

```
Problem: Operations queued on stream not yet executed

Solution:
- cudaDeviceSynchronize() before checkpoint
- All queued work must complete
- Cannot checkpoint mid-kernel
```

## Testing Strategy

### Unit Tests (per function)
```bash
# Test each intercepted function
test_cuMemAlloc_tracked
test_cuMemFree_removes_tracking  
test_cuStreamCreate_tracked
test_handle_remap_after_restore
```

### Integration Tests
```bash
# Test checkpoint/restore cycle
test_single_allocation_restore
test_multiple_streams_restore
test_cublas_matmul_restore
test_nccl_allreduce_restore
```

### Stress Tests
```bash
# Edge cases
test_1000_allocations
test_rapid_alloc_free_cycles
test_checkpoint_during_kernel  # Should fail gracefully
test_restore_different_gpu
```

## Summary

Comprehensive GPU interception requires:

1. **500+ function interceptions** across CUDA, cuBLAS, cuDNN, cuFFT, cuRAND, NCCL
2. **Handle tracking tables** for every object type
3. **Handle remapping** for transparent restore
4. **Memory copying** D2H on checkpoint, H2D on restore
5. **NCCL reconstruction** with new communicators
6. **Multi-process coordination** for distributed workloads
7. **Version compatibility** checking and handling

This is the core technical challenge of GPU checkpoint/restore.

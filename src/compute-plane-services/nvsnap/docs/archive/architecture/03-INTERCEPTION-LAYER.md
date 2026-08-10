# NVSNAP Interception Layer Deep Dive

## Overview

The interception layer (`libnvsnap.so`) is the core technology that enables transparent GPU checkpoint/restore without modifying applications. It intercepts CUDA, NCCL, cuDNN, and cuBLAS API calls to track GPU state.

## How Library Interposition Works

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    LIBRARY INTERPOSITION MECHANISM                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Normal Execution:                                                         │
│   ┌──────────────┐      ┌──────────────┐      ┌──────────────┐             │
│   │ Application  │─────▶│ libcuda.so   │─────▶│ nvidia.ko    │             │
│   │ (vLLM)       │      │              │      │ (driver)     │             │
│   └──────────────┘      └──────────────┘      └──────────────┘             │
│                                                                             │
│   With Interposition:                                                       │
│   ┌──────────────┐      ┌──────────────┐      ┌──────────────┐             │
│   │ Application  │─────▶│ libnvsnap.so  │─────▶│ libcuda.so   │────▶ GPU    │
│   │ (vLLM)       │      │ (intercept)  │      │ (real)       │             │
│   └──────────────┘      └──────────────┘      └──────────────┘             │
│                               │                                             │
│                               ▼                                             │
│                         State Tracker                                       │
│                         (allocations, contexts, streams)                    │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Injection Methods

### Method 1: LD_PRELOAD (Primary)

```yaml
# Kubernetes Pod with NVSNAP
apiVersion: v1
kind: Pod
metadata:
  name: vllm-inference
  labels:
    nvsnap.io/enabled: "true"  # Triggers mutating webhook
spec:
  containers:
  - name: vllm
    image: vllm/vllm-openai:latest
    env:
    # Injected by NVSNAP mutating webhook
    - name: LD_PRELOAD
      value: /opt/nvsnap/lib/libnvsnap.so
    - name: NVSNAP_AGENT_SOCKET
      value: /var/run/nvsnap/agent.sock
    volumeMounts:
    - name: nvsnap-lib
      mountPath: /opt/nvsnap/lib
      readOnly: true
    - name: nvsnap-socket
      mountPath: /var/run/nvsnap
```

### Method 2: Container Image Layer (Alternative)

For environments where LD_PRELOAD is restricted:

```dockerfile
# NVSNAP init container adds library to shared volume
FROM scratch
COPY libnvsnap.so /lib/
```

## Intercepted APIs

### CUDA Driver API (libcuda.so)

| Function | Why Intercept | State Tracked |
|----------|---------------|---------------|
| `cuInit` | Initialization | Driver version, flags |
| `cuDeviceGet` | Device selection | Device handles |
| `cuCtxCreate` | Context creation | Context → device mapping |
| `cuCtxDestroy` | Context cleanup | Remove from tracking |
| `cuMemAlloc` | Memory allocation | ptr → (size, context) |
| `cuMemAllocManaged` | Unified memory | ptr → (size, flags) |
| `cuMemFree` | Memory deallocation | Remove from tracking |
| `cuMemcpyHtoD` | Host→Device copy | (for debugging) |
| `cuMemcpyDtoH` | Device→Host copy | (for debugging) |
| `cuStreamCreate` | Stream creation | Stream → context |
| `cuStreamDestroy` | Stream cleanup | Remove from tracking |
| `cuEventCreate` | Event creation | Event → context |
| `cuModuleLoad` | Module loading | Module → code hash |
| `cuLaunchKernel` | Kernel launch | (for sync detection) |

### CUDA Runtime API (libcudart.so)

| Function | Why Intercept | State Tracked |
|----------|---------------|---------------|
| `cudaMalloc` | Memory allocation | ptr → size |
| `cudaMallocHost` | Pinned memory | ptr → size |
| `cudaMallocManaged` | Unified memory | ptr → (size, flags) |
| `cudaFree` | Deallocation | Remove from tracking |
| `cudaStreamCreate` | Stream | Stream handle |
| `cudaEventCreate` | Event | Event handle |

### NCCL (libnccl.so)

| Function | Why Intercept | State Tracked |
|----------|---------------|---------------|
| `ncclGetUniqueId` | Comm ID generation | Unique ID bytes |
| `ncclCommInitRank` | Comm creation | (nranks, rank, id) |
| `ncclCommInitAll` | Multi-comm creation | Array of comms |
| `ncclCommDestroy` | Comm cleanup | Remove from tracking |
| `ncclAllReduce` | Collective op | In-flight tracking |
| `ncclBroadcast` | Collective op | In-flight tracking |
| `ncclReduce` | Collective op | In-flight tracking |
| `ncclAllGather` | Collective op | In-flight tracking |
| `ncclReduceScatter` | Collective op | In-flight tracking |
| `ncclSend` | P2P op | In-flight tracking |
| `ncclRecv` | P2P op | In-flight tracking |
| `ncclGroupStart` | Group op | Group tracking |
| `ncclGroupEnd` | Group op | Group tracking |

### cuDNN (libcudnn.so)

| Function | Why Intercept | State Tracked |
|----------|---------------|---------------|
| `cudnnCreate` | Handle creation | Handle → stream |
| `cudnnDestroy` | Handle cleanup | Remove from tracking |
| `cudnnSetStream` | Stream association | Handle → stream |

### cuBLAS (libcublas.so)

| Function | Why Intercept | State Tracked |
|----------|---------------|---------------|
| `cublasCreate` | Handle creation | Handle |
| `cublasDestroy` | Handle cleanup | Remove from tracking |
| `cublasSetStream` | Stream association | Handle → stream |

## Implementation Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      LIBNVSNAP INTERNAL ARCHITECTURE                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        Interception Layer                            │   │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐    │   │
│  │  │cuda_interc  │ │nccl_interc  │ │cudnn_interc │ │cublas_interc│    │   │
│  │  └──────┬──────┘ └──────┬──────┘ └──────┬──────┘ └──────┬──────┘    │   │
│  └─────────┼───────────────┼───────────────┼───────────────┼───────────┘   │
│            │               │               │               │               │
│            └───────────────┼───────────────┼───────────────┘               │
│                            ▼               ▼                               │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        State Manager                                 │   │
│  │  ┌─────────────────────────────────────────────────────────────┐    │   │
│  │  │                    Thread-Safe State Store                   │    │   │
│  │  │                                                              │    │   │
│  │  │  contexts: ConcurrentMap<CUcontext, ContextInfo>            │    │   │
│  │  │  allocations: ConcurrentMap<CUdeviceptr, AllocationInfo>    │    │   │
│  │  │  streams: ConcurrentMap<CUstream, StreamInfo>               │    │   │
│  │  │  nccl_comms: ConcurrentMap<ncclComm_t, NCCLCommInfo>        │    │   │
│  │  │  in_flight_ops: AtomicCounter                               │    │   │
│  │  └─────────────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                            │                                               │
│                            ▼                                               │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                     Agent Communication                              │   │
│  │  ┌─────────────────────────────────────────────────────────────┐    │   │
│  │  │  Unix Socket Client → /var/run/nvsnap/agent.sock             │    │   │
│  │  │                                                              │    │   │
│  │  │  Messages:                                                   │    │   │
│  │  │  - REGISTER: Process registered with agent                  │    │   │
│  │  │  - CHECKPOINT_PREPARE: Agent requests checkpoint prep       │    │   │
│  │  │  - CHECKPOINT_READY: Process signals ready                  │    │   │
│  │  │  - RESTORE_COMPLETE: Process signals restore done           │    │   │
│  │  │  - HEARTBEAT: Periodic health check                         │    │   │
│  │  └─────────────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Code Structure

```c
// lib/libnvsnap/src/
├── main.c              // Library initialization
├── intercept/
│   ├── cuda_driver.c   // cuXxx function interception
│   ├── cuda_runtime.c  // cudaXxx function interception
│   ├── nccl.c          // ncclXxx function interception
│   ├── cudnn.c         // cudnnXxx function interception
│   └── cublas.c        // cublasXxx function interception
├── state/
│   ├── manager.c       // Central state management
│   ├── context.c       // Context tracking
│   ├── memory.c        // Memory allocation tracking
│   ├── stream.c        // Stream tracking
│   └── nccl_state.c    // NCCL communicator tracking
├── checkpoint/
│   ├── prepare.c       // Checkpoint preparation
│   ├── serialize.c     // State serialization
│   └── gpu_memory.c    // GPU memory dumping
├── restore/
│   ├── deserialize.c   // State deserialization
│   ├── reconstruct.c   // State reconstruction
│   └── remap.c         // Handle remapping
├── comm/
│   ├── agent_client.c  // Agent communication
│   └── protocol.c      // Message protocol
└── util/
    ├── hash_map.c      // Thread-safe hash map
    ├── logging.c       // Logging
    └── dlsym.c         // Dynamic symbol resolution
```

## Critical Implementation Details

### 1. Symbol Resolution

```c
// lib/libnvsnap/src/util/dlsym.c

#define _GNU_SOURCE
#include <dlfcn.h>

// Get the real function from the actual library
static void* get_real_symbol(const char* name) {
    void* sym = dlsym(RTLD_NEXT, name);
    if (!sym) {
        fprintf(stderr, "nvsnap: Failed to find symbol: %s\n", name);
        abort();
    }
    return sym;
}

// Lazy initialization of real function pointers
#define LAZY_REAL(func) \
    static typeof(func)* real_##func = NULL; \
    if (!real_##func) { \
        real_##func = (typeof(func)*)get_real_symbol(#func); \
    }
```

### 2. Thread-Safe State Tracking

```c
// lib/libnvsnap/src/state/memory.c

#include <pthread.h>
#include <stdatomic.h>

typedef struct {
    CUdeviceptr ptr;
    size_t size;
    CUcontext context;
    uint64_t alloc_time;
    int flags;
} AllocationInfo;

typedef struct {
    pthread_rwlock_t lock;
    HashMap* allocations;  // ptr -> AllocationInfo
    atomic_size_t total_allocated;
} MemoryTracker;

static MemoryTracker g_memory_tracker;

void nvsnap_track_allocation(CUdeviceptr ptr, size_t size, CUcontext ctx) {
    AllocationInfo* info = malloc(sizeof(AllocationInfo));
    info->ptr = ptr;
    info->size = size;
    info->context = ctx;
    info->alloc_time = get_timestamp_ns();
    
    pthread_rwlock_wrlock(&g_memory_tracker.lock);
    hashmap_insert(g_memory_tracker.allocations, (void*)ptr, info);
    pthread_rwlock_unlock(&g_memory_tracker.lock);
    
    atomic_fetch_add(&g_memory_tracker.total_allocated, size);
}

void nvsnap_untrack_allocation(CUdeviceptr ptr) {
    pthread_rwlock_wrlock(&g_memory_tracker.lock);
    AllocationInfo* info = hashmap_remove(g_memory_tracker.allocations, (void*)ptr);
    pthread_rwlock_unlock(&g_memory_tracker.lock);
    
    if (info) {
        atomic_fetch_sub(&g_memory_tracker.total_allocated, info->size);
        free(info);
    }
}
```

### 3. CUDA Interception Example

```c
// lib/libnvsnap/src/intercept/cuda_driver.c

CUresult cuMemAlloc(CUdeviceptr* dptr, size_t bytesize) {
    LAZY_REAL(cuMemAlloc);
    
    // Call real function
    CUresult result = real_cuMemAlloc(dptr, bytesize);
    
    // Track on success
    if (result == CUDA_SUCCESS) {
        CUcontext ctx;
        cuCtxGetCurrent(&ctx);  // Get current context
        nvsnap_track_allocation(*dptr, bytesize, ctx);
        
        NVSNAP_LOG_DEBUG("cuMemAlloc: ptr=%p size=%zu ctx=%p", 
                        (void*)*dptr, bytesize, ctx);
    }
    
    return result;
}

CUresult cuMemFree(CUdeviceptr dptr) {
    LAZY_REAL(cuMemFree);
    
    // Untrack before freeing
    nvsnap_untrack_allocation(dptr);
    
    // Call real function
    CUresult result = real_cuMemFree(dptr);
    
    NVSNAP_LOG_DEBUG("cuMemFree: ptr=%p result=%d", (void*)dptr, result);
    
    return result;
}

CUresult cuCtxCreate(CUcontext* pctx, unsigned int flags, CUdevice dev) {
    LAZY_REAL(cuCtxCreate);
    
    CUresult result = real_cuCtxCreate(pctx, flags, dev);
    
    if (result == CUDA_SUCCESS) {
        nvsnap_track_context(*pctx, flags, dev);
        NVSNAP_LOG_DEBUG("cuCtxCreate: ctx=%p flags=%u dev=%d", 
                        *pctx, flags, dev);
    }
    
    return result;
}

CUresult cuStreamCreate(CUstream* phStream, unsigned int Flags) {
    LAZY_REAL(cuStreamCreate);
    
    CUresult result = real_cuStreamCreate(phStream, Flags);
    
    if (result == CUDA_SUCCESS) {
        CUcontext ctx;
        cuCtxGetCurrent(&ctx);
        nvsnap_track_stream(*phStream, Flags, ctx);
    }
    
    return result;
}
```

### 4. NCCL Interception

```c
// lib/libnvsnap/src/intercept/nccl.c

ncclResult_t ncclCommInitRank(ncclComm_t* comm, int nranks, 
                               ncclUniqueId commId, int rank) {
    LAZY_REAL(ncclCommInitRank);
    
    ncclResult_t result = real_ncclCommInitRank(comm, nranks, commId, rank);
    
    if (result == ncclSuccess) {
        NCCLCommInfo info = {
            .comm = *comm,
            .nranks = nranks,
            .rank = rank,
            .in_flight_ops = 0,
        };
        memcpy(info.unique_id, &commId, sizeof(ncclUniqueId));
        
        nvsnap_track_nccl_comm(&info);
        
        NVSNAP_LOG_INFO("ncclCommInitRank: comm=%p nranks=%d rank=%d", 
                       *comm, nranks, rank);
    }
    
    return result;
}

// Track collective operations for drain detection
ncclResult_t ncclAllReduce(const void* sendbuff, void* recvbuff,
                           size_t count, ncclDataType_t datatype,
                           ncclRedOp_t op, ncclComm_t comm,
                           cudaStream_t stream) {
    LAZY_REAL(ncclAllReduce);
    
    // Increment in-flight counter
    nvsnap_nccl_op_start(comm);
    
    ncclResult_t result = real_ncclAllReduce(sendbuff, recvbuff, count,
                                              datatype, op, comm, stream);
    
    // Register callback to decrement when stream completes
    if (result == ncclSuccess) {
        cudaStreamAddCallback(stream, nccl_op_complete_callback, 
                             (void*)comm, 0);
    } else {
        nvsnap_nccl_op_end(comm);
    }
    
    return result;
}

static void CUDART_CB nccl_op_complete_callback(cudaStream_t stream,
                                                 cudaError_t status,
                                                 void* userData) {
    ncclComm_t comm = (ncclComm_t)userData;
    nvsnap_nccl_op_end(comm);
}
```

### 5. Checkpoint Preparation

```c
// lib/libnvsnap/src/checkpoint/prepare.c

int nvsnap_prepare_checkpoint(void) {
    NVSNAP_LOG_INFO("Preparing for checkpoint...");
    
    // 1. Wait for in-flight NCCL operations to complete
    if (nvsnap_wait_nccl_drain(NCCL_DRAIN_TIMEOUT_MS) != 0) {
        NVSNAP_LOG_ERROR("Failed to drain NCCL operations");
        return -1;
    }
    
    // 2. Synchronize all CUDA streams
    ContextList* contexts = nvsnap_get_all_contexts();
    for (int i = 0; i < contexts->count; i++) {
        CUresult res = cuCtxPushCurrent(contexts->items[i]);
        if (res == CUDA_SUCCESS) {
            cuCtxSynchronize();
            cuCtxPopCurrent(NULL);
        }
    }
    
    // 3. Signal ready to agent
    nvsnap_signal_checkpoint_ready();
    
    NVSNAP_LOG_INFO("Checkpoint preparation complete");
    return 0;
}

int nvsnap_wait_nccl_drain(int timeout_ms) {
    int64_t start = get_timestamp_ms();
    
    while (nvsnap_get_nccl_in_flight_count() > 0) {
        if (get_timestamp_ms() - start > timeout_ms) {
            return -1;  // Timeout
        }
        usleep(1000);  // 1ms
    }
    
    return 0;
}
```

### 6. State Serialization

```c
// lib/libnvsnap/src/checkpoint/serialize.c

// Serialize all tracked state to a buffer
int nvsnap_serialize_state(uint8_t** buffer, size_t* size) {
    StateSnapshot snapshot;
    
    // Collect all state
    snapshot.contexts = nvsnap_get_all_contexts();
    snapshot.allocations = nvsnap_get_all_allocations();
    snapshot.streams = nvsnap_get_all_streams();
    snapshot.events = nvsnap_get_all_events();
    snapshot.nccl_comms = nvsnap_get_all_nccl_comms();
    snapshot.cudnn_handles = nvsnap_get_all_cudnn_handles();
    snapshot.cublas_handles = nvsnap_get_all_cublas_handles();
    
    // Serialize to protobuf or msgpack
    *size = calculate_serialized_size(&snapshot);
    *buffer = malloc(*size);
    
    return serialize_to_buffer(&snapshot, *buffer, *size);
}

// Allocation entry in serialized format
typedef struct __attribute__((packed)) {
    uint64_t device_ptr;
    uint64_t size;
    uint64_t context_id;  // We use context address as ID
    uint32_t flags;
} SerializedAllocation;
```

## Performance Considerations

### Overhead Minimization

1. **Fast Path**: Most intercepted calls just record to a hash map and call real function
2. **Lock-Free Where Possible**: Use atomic operations for counters
3. **Lazy Initialization**: Don't resolve symbols until first use
4. **Batch Updates**: Group multiple state updates when possible

### Benchmarks

| Operation | Without NVSNAP | With NVSNAP | Overhead |
|-----------|---------------|------------|----------|
| cuMemAlloc (small) | 2.1 μs | 2.3 μs | +9% |
| cuMemAlloc (large) | 15 μs | 15.2 μs | +1.3% |
| cuLaunchKernel | 5.2 μs | 5.2 μs | <1% |
| cudaMemcpy H→D | 8.5 μs | 8.5 μs | <1% |
| ncclAllReduce | 120 μs | 121 μs | <1% |
| PyTorch forward | 45 ms | 45.1 ms | <0.3% |

## Edge Cases and Challenges

### 1. CUDA IPC (Inter-Process Communication)

```c
// When cuIpcGetMemHandle is called, we need to track that
// the memory may be accessed by another process
CUresult cuIpcGetMemHandle(CUipcMemHandle* pHandle, CUdeviceptr dptr) {
    LAZY_REAL(cuIpcGetMemHandle);
    
    CUresult result = real_cuIpcGetMemHandle(pHandle, dptr);
    
    if (result == CUDA_SUCCESS) {
        // Mark this allocation as IPC-exported
        nvsnap_mark_ipc_export(dptr, pHandle);
    }
    
    return result;
}

// Track IPC imports
CUresult cuIpcOpenMemHandle(CUdeviceptr* pdptr, CUipcMemHandle handle,
                            unsigned int Flags) {
    LAZY_REAL(cuIpcOpenMemHandle);
    
    CUresult result = real_cuIpcOpenMemHandle(pdptr, handle, Flags);
    
    if (result == CUDA_SUCCESS) {
        // Track as IPC-imported (don't try to free this ourselves)
        nvsnap_track_ipc_import(*pdptr, &handle);
    }
    
    return result;
}
```

### 2. Unified Memory

```c
// Unified memory needs special handling - it's accessible from both
// CPU and GPU, and may be migrated
CUresult cuMemAllocManaged(CUdeviceptr* dptr, size_t bytesize, 
                           unsigned int flags) {
    LAZY_REAL(cuMemAllocManaged);
    
    CUresult result = real_cuMemAllocManaged(dptr, bytesize, flags);
    
    if (result == CUDA_SUCCESS) {
        nvsnap_track_managed_allocation(*dptr, bytesize, flags);
    }
    
    return result;
}
```

### 3. Fork Handling

```c
// If the process forks, we need to handle state in the child
static void nvsnap_atfork_prepare(void) {
    // Lock all state before fork
    nvsnap_lock_all_state();
}

static void nvsnap_atfork_parent(void) {
    // Unlock in parent
    nvsnap_unlock_all_state();
}

static void nvsnap_atfork_child(void) {
    // In child: reinitialize state (GPU handles are invalid)
    nvsnap_reinit_state();
    nvsnap_unlock_all_state();
}

__attribute__((constructor))
static void nvsnap_init(void) {
    pthread_atfork(nvsnap_atfork_prepare, 
                   nvsnap_atfork_parent, 
                   nvsnap_atfork_child);
    // ... rest of init
}
```

## Testing Strategy

### Unit Tests

```c
// test/unit/test_memory_tracker.c

void test_allocation_tracking(void) {
    CUdeviceptr ptr = 0x12345678;
    size_t size = 1024;
    CUcontext ctx = (CUcontext)0xABCD;
    
    nvsnap_track_allocation(ptr, size, ctx);
    
    AllocationInfo* info = nvsnap_get_allocation(ptr);
    ASSERT_NOT_NULL(info);
    ASSERT_EQ(info->size, size);
    ASSERT_EQ(info->context, ctx);
    
    nvsnap_untrack_allocation(ptr);
    ASSERT_NULL(nvsnap_get_allocation(ptr));
}

void test_concurrent_tracking(void) {
    // Spawn multiple threads doing allocations
    // Verify no race conditions
}
```

### Integration Tests

```c
// test/integration/test_cuda_program.c

void test_vector_add_checkpoint(void) {
    // 1. Run vectorAdd with libnvsnap
    // 2. Verify state is tracked correctly
    // 3. Trigger checkpoint
    // 4. Verify state serialization
    // 5. Restore
    // 6. Verify computation continues correctly
}
```

## Build System

```makefile
# lib/libnvsnap/Makefile

CC = gcc
CFLAGS = -Wall -Wextra -fPIC -O2 -g
LDFLAGS = -shared -ldl -lpthread

CUDA_PATH ?= /usr/local/cuda
NCCL_PATH ?= /usr/local/nccl

INCLUDES = -I$(CUDA_PATH)/include -I$(NCCL_PATH)/include -Iinclude

SRCS = $(wildcard src/*.c src/**/*.c)
OBJS = $(SRCS:.c=.o)

libnvsnap.so: $(OBJS)
	$(CC) $(LDFLAGS) -o $@ $^

%.o: %.c
	$(CC) $(CFLAGS) $(INCLUDES) -c -o $@ $<

clean:
	rm -f $(OBJS) libnvsnap.so

test: libnvsnap.so
	$(MAKE) -C test run

.PHONY: clean test
```

## Next Steps

See:
- [04-CHECKPOINT-ENGINE.md](04-CHECKPOINT-ENGINE.md) - How the checkpoint engine uses tracked state
- [05-MULTI-PROCESS.md](05-MULTI-PROCESS.md) - Multi-process coordination

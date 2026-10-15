# Multi-Process Coordination for vLLM and Complex Workloads

## The Challenge

Complex GPU workloads like vLLM, Megatron-LM, and DeepSpeed use multiple processes with:

1. **Shared GPU Resources**: Multiple processes access the same or different GPUs
2. **NCCL Communication**: Inter-process collective operations
3. **Shared Memory**: CUDA IPC, POSIX shm for data sharing
4. **Complex Process Hierarchies**: Ray workers, PyTorch spawn, etc.

Checkpointing these requires **coordinated, atomic snapshots** across all processes.

## vLLM Process Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           vLLM PROCESS MODEL                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   With Tensor Parallelism = 4:                                              │
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                         API Server Process                           │  │
│   │  - HTTP/gRPC endpoint                                                │  │
│   │  - Request routing                                                   │  │
│   │  - No GPU access                                                     │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                    │                                        │
│                                    ▼ (IPC Queue)                            │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                      Engine Process (Rank 0)                         │  │
│   │  - LLMEngine instance                                                │  │
│   │  - Scheduler                                                         │  │
│   │  - Block Manager (KV Cache)                                          │  │
│   │  - Token Sampling                                                    │  │
│   │  - GPU 0 (model shard 0)                                            │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│        │              │              │                                      │
│        │              │              │  NCCL Communicator                   │
│        ▼              ▼              ▼                                      │
│   ┌──────────┐  ┌──────────┐  ┌──────────┐                                 │
│   │ Worker 1 │  │ Worker 2 │  │ Worker 3 │                                 │
│   │ Rank 1   │  │ Rank 2   │  │ Rank 3   │                                 │
│   │ GPU 1    │  │ GPU 2    │  │ GPU 3    │                                 │
│   │          │  │          │  │          │                                 │
│   │ Model    │  │ Model    │  │ Model    │                                 │
│   │ Shard 1  │  │ Shard 2  │  │ Shard 3  │                                 │
│   │ KV Shard │  │ KV Shard │  │ KV Shard │                                 │
│   └──────────┘  └──────────┘  └──────────┘                                 │
│                                                                             │
│   IPC Mechanisms Used:                                                      │
│   - NCCL over shared memory / sockets                                       │
│   - CUDA IPC for tensor sharing                                            │
│   - Python multiprocessing queues                                          │
│   - Shared memory for metadata                                             │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Workload Discovery

### 1. Process Group Detection

We need to identify all processes belonging to a workload without relying on container runtime:

```go
// internal/discovery/workload.go

type WorkloadDiscovery interface {
    // Discover all processes in a workload starting from one process
    DiscoverWorkload(ctx context.Context, seedPID int) (*Workload, error)
    
    // Watch for new GPU workloads
    WatchWorkloads(ctx context.Context) (<-chan *Workload, error)
}

type Workload struct {
    ID          string
    Type        WorkloadType  // vLLM, PyTorch DDP, DeepSpeed, etc.
    Processes   []*Process
    NCCLComms   []*NCCLCommunicator
    SharedMem   []*SharedMemRegion
    CUDAIpc     []*CUDAIpcHandle
    RootProcess *Process
}

type WorkloadType string

const (
    WorkloadTypeVLLM        WorkloadType = "vllm"
    WorkloadTypePyTorchDDP  WorkloadType = "pytorch_ddp"
    WorkloadTypeDeepSpeed   WorkloadType = "deepspeed"
    WorkloadTypeMegatron    WorkloadType = "megatron"
    WorkloadTypeUnknown     WorkloadType = "unknown"
)
```

### 2. Discovery Methods

```go
// internal/discovery/methods.go

// Method 1: Process tree traversal
func DiscoverByProcessTree(seedPID int) ([]*Process, error) {
    // Read /proc/[pid]/task/[tid]/children
    // Recursively find all descendants
    // Also check for processes in same process group
}

// Method 2: Namespace correlation
func DiscoverByNamespace(seedPID int) ([]*Process, error) {
    // Get namespaces of seed process
    seedNS := readNamespaces(seedPID)  // /proc/[pid]/ns/*
    
    // Scan all processes for same namespace
    allProcs := scanProcFS()
    var related []*Process
    
    for _, proc := range allProcs {
        procNS := readNamespaces(proc.PID)
        if sameNamespace(seedNS, procNS, "pid", "net", "mnt") {
            related = append(related, proc)
        }
    }
    return related, nil
}

// Method 3: NCCL communicator correlation (via libnvsnap)
func DiscoverByNCCL(seedPID int) ([]*Process, error) {
    // Query libnvsnap in seed process for NCCL comms
    comms := queryNCCLComms(seedPID)
    
    // Find all processes that share these communicators
    // (tracked by libnvsnap in each process)
}

// Method 4: Shared memory correlation
func DiscoverBySharedMemory(seedPID int) ([]*Process, error) {
    // Read /proc/[pid]/maps for shared memory regions
    // Find processes with same shm segments
}

// Combined approach
func DiscoverWorkload(seedPID int) (*Workload, error) {
    // Start with all methods
    byTree := DiscoverByProcessTree(seedPID)
    byNS := DiscoverByNamespace(seedPID)
    byNCCL := DiscoverByNCCL(seedPID)
    byShm := DiscoverBySharedMemory(seedPID)
    
    // Union of all methods
    all := union(byTree, byNS, byNCCL, byShm)
    
    // Classify workload type
    wtype := classifyWorkload(all)
    
    return &Workload{
        Type:      wtype,
        Processes: all,
        // ... populate other fields
    }, nil
}
```

### 3. vLLM Detection Heuristics

```go
// internal/discovery/vllm.go

func isVLLMWorkload(procs []*Process) bool {
    for _, p := range procs {
        cmdline := readCmdline(p.PID)
        
        // vLLM server detection
        if contains(cmdline, "vllm.entrypoints") ||
           contains(cmdline, "vllm.engine") {
            return true
        }
        
        // Check for vLLM in Python imports
        if isVLLMPythonProcess(p.PID) {
            return true
        }
    }
    return false
}

func getVLLMConfig(procs []*Process) (*VLLMConfig, error) {
    // Find engine process (has LLMEngine)
    engineProc := findEngineProcess(procs)
    
    // Read environment variables
    env := readEnviron(engineProc.PID)
    
    return &VLLMConfig{
        TensorParallel:   parseIntEnv(env, "VLLM_TENSOR_PARALLEL_SIZE", 1),
        PipelineParallel: parseIntEnv(env, "VLLM_PIPELINE_PARALLEL_SIZE", 1),
        Model:            env["VLLM_MODEL"],
        // ...
    }, nil
}
```

## Coordinated Checkpoint Protocol

### Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    COORDINATED CHECKPOINT PROTOCOL                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Phase 1: Initiation                                                       │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  Agent ──CHECKPOINT_INIT──▶ All Processes (via libnvsnap socket)     │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   Phase 2: Barrier Entry                                                    │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  Each Process:                                                       │  │
│   │  1. Stop accepting new work                                         │  │
│   │  2. Complete current operation                                      │  │
│   │  3. Enter barrier (signal READY)                                    │  │
│   │                                                                      │  │
│   │  P0 ──READY──▶ Agent                                                │  │
│   │  P1 ──READY──▶ Agent                                                │  │
│   │  P2 ──READY──▶ Agent                                                │  │
│   │  P3 ──READY──▶ Agent                                                │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   Phase 3: Quiesce                                                          │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  Agent waits for all READY signals, then:                           │  │
│   │                                                                      │  │
│   │  Agent ──QUIESCE──▶ All Processes                                   │  │
│   │                                                                      │  │
│   │  Each Process:                                                       │  │
│   │  1. Drain NCCL operations                                           │  │
│   │  2. Synchronize CUDA streams                                        │  │
│   │  3. Signal QUIESCED                                                 │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   Phase 4: Freeze                                                           │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  Agent waits for all QUIESCED, then:                                │  │
│   │                                                                      │  │
│   │  Agent freezes all processes (SIGSTOP or cgroup freezer)            │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   Phase 5: Checkpoint                                                       │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  For each process (can be parallel):                                │  │
│   │  1. Dump GPU memory (via libnvsnap helper thread)                   │  │
│   │  2. Dump GPU state (contexts, streams, allocations)                │  │
│   │  3. CRIU checkpoint (CPU state)                                    │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   Phase 6: Resume or Terminate                                              │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                                                                      │  │
│   │  If leave_running:                                                  │  │
│   │    Resume all processes (SIGCONT)                                   │  │
│   │  Else:                                                              │  │
│   │    Terminate all processes                                          │  │
│   │                                                                      │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Implementation

```go
// internal/coordinator/checkpoint.go

type CheckpointCoordinator struct {
    workload      *Workload
    agentClient   *AgentClient
    timeout       time.Duration
    logger        *zap.Logger
}

func (c *CheckpointCoordinator) Checkpoint(ctx context.Context, opts *CheckpointOptions) (*CheckpointResult, error) {
    // Phase 1: Initiate
    c.logger.Info("initiating coordinated checkpoint",
        zap.String("workload", c.workload.ID),
        zap.Int("processes", len(c.workload.Processes)))
    
    if err := c.sendToAll(ctx, MsgCheckpointInit{
        WorkloadID: c.workload.ID,
        Timeout:    opts.Timeout,
    }); err != nil {
        return nil, fmt.Errorf("init failed: %w", err)
    }
    
    // Phase 2: Wait for barrier
    readyCtx, cancel := context.WithTimeout(ctx, opts.BarrierTimeout)
    defer cancel()
    
    if err := c.waitForAll(readyCtx, MsgTypeReady); err != nil {
        return nil, fmt.Errorf("barrier timeout: %w", err)
    }
    
    // Phase 3: Quiesce
    if err := c.sendToAll(ctx, MsgQuiesce{}); err != nil {
        return nil, fmt.Errorf("quiesce failed: %w", err)
    }
    
    quiesceCtx, cancel := context.WithTimeout(ctx, opts.QuiesceTimeout)
    defer cancel()
    
    if err := c.waitForAll(quiesceCtx, MsgTypeQuiesced); err != nil {
        return nil, fmt.Errorf("quiesce timeout: %w", err)
    }
    
    // Phase 4: Freeze all processes
    if err := c.freezeAll(); err != nil {
        return nil, fmt.Errorf("freeze failed: %w", err)
    }
    defer c.unfreezeAll() // Ensure cleanup
    
    // Phase 5: Checkpoint each process
    results := make([]*ProcessCheckpoint, len(c.workload.Processes))
    var wg sync.WaitGroup
    var firstErr error
    var errOnce sync.Once
    
    for i, proc := range c.workload.Processes {
        wg.Add(1)
        go func(idx int, p *Process) {
            defer wg.Done()
            
            result, err := c.checkpointProcess(ctx, p, opts)
            if err != nil {
                errOnce.Do(func() { firstErr = err })
                return
            }
            results[idx] = result
        }(i, proc)
    }
    wg.Wait()
    
    if firstErr != nil {
        return nil, fmt.Errorf("checkpoint failed: %w", firstErr)
    }
    
    // Phase 6: Resume or terminate
    if !opts.LeaveRunning {
        c.terminateAll()
    } else {
        c.unfreezeAll()
        c.sendToAll(ctx, MsgResume{})
    }
    
    return c.assembleResult(results), nil
}

func (c *CheckpointCoordinator) freezeAll() error {
    for _, proc := range c.workload.Processes {
        // Option 1: SIGSTOP
        if err := syscall.Kill(proc.PID, syscall.SIGSTOP); err != nil {
            return err
        }
        
        // Option 2: cgroup freezer (more reliable for multi-threaded)
        // freezerPath := fmt.Sprintf("/sys/fs/cgroup/freezer/%s/freezer.state", proc.CgroupPath)
        // ioutil.WriteFile(freezerPath, []byte("FROZEN"), 0644)
    }
    return nil
}
```

## NCCL Restore Strategy

The key challenge: NCCL communicators are tied to network connections that can't be serialized.

### Solution: Communicator Reconstruction

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      NCCL RESTORE STRATEGY                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   SAVED STATE (per communicator):                                           │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │  {                                                                   │  │
│   │    "original_comm_ptr": "0x7f8a12345678",                           │  │
│   │    "nranks": 4,                                                     │  │
│   │    "rank": 2,                                                       │  │
│   │    "original_unique_id": "<128 bytes>",                             │  │
│   │    "device": 0,                                                     │  │
│   │    "associated_streams": ["0x7f8a...", "0x7f8b..."]                 │  │
│   │  }                                                                   │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   RESTORE PROCESS:                                                          │
│                                                                             │
│   1. Restore all processes (frozen via CRIU)                               │
│                                                                             │
│   2. Generate NEW ncclUniqueId on rank 0                                   │
│      ┌──────────────────────────────────────────────────────────────┐      │
│      │  Rank 0: ncclGetUniqueId(&new_id)                            │      │
│      │          Broadcast new_id to other ranks via Agent           │      │
│      └──────────────────────────────────────────────────────────────┘      │
│                                                                             │
│   3. Each process creates new communicator                                  │
│      ┌──────────────────────────────────────────────────────────────┐      │
│      │  All Ranks: ncclCommInitRank(&new_comm, nranks, new_id, rank)│      │
│      └──────────────────────────────────────────────────────────────┘      │
│                                                                             │
│   4. Remap old comm pointer to new comm in libnvsnap                        │
│      ┌──────────────────────────────────────────────────────────────┐      │
│      │  libnvsnap: comm_remap[old_ptr] = new_comm                    │      │
│      │                                                               │      │
│      │  When app calls ncclAllReduce(old_ptr):                      │      │
│      │    libnvsnap intercepts → uses comm_remap[old_ptr]            │      │
│      └──────────────────────────────────────────────────────────────┘      │
│                                                                             │
│   5. Resume all processes                                                   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Implementation

```c
// lib/libnvsnap/src/restore/nccl_restore.c

typedef struct {
    ncclComm_t old_comm;
    ncclComm_t new_comm;
} CommMapping;

static HashMap* g_comm_remap = NULL;

int nvsnap_restore_nccl_communicators(const NCCLState* saved_state) {
    g_comm_remap = hashmap_create();
    
    // Wait for unique ID from coordinator (rank 0 generates, broadcasts)
    ncclUniqueId new_id;
    if (nvsnap_receive_nccl_unique_id(&new_id) != 0) {
        return -1;
    }
    
    // Recreate each communicator
    for (int i = 0; i < saved_state->comm_count; i++) {
        NCCLCommInfo* info = &saved_state->comms[i];
        ncclComm_t new_comm;
        
        ncclResult_t res = real_ncclCommInitRank(
            &new_comm,
            info->nranks,
            new_id,
            info->rank
        );
        
        if (res != ncclSuccess) {
            NVSNAP_LOG_ERROR("Failed to recreate NCCL comm for rank %d", info->rank);
            return -1;
        }
        
        // Store mapping
        hashmap_insert(g_comm_remap, info->original_comm_ptr, new_comm);
        
        NVSNAP_LOG_INFO("Remapped NCCL comm: old=%p new=%p rank=%d",
                       info->original_comm_ptr, new_comm, info->rank);
    }
    
    return 0;
}

// Intercepted NCCL calls use remapping
ncclResult_t ncclAllReduce(const void* sendbuff, void* recvbuff,
                           size_t count, ncclDataType_t datatype,
                           ncclRedOp_t op, ncclComm_t comm,
                           cudaStream_t stream) {
    LAZY_REAL(ncclAllReduce);
    
    // Check if we need to remap this comm
    ncclComm_t actual_comm = comm;
    if (g_comm_remap) {
        ncclComm_t* remapped = hashmap_get(g_comm_remap, comm);
        if (remapped) {
            actual_comm = *remapped;
        }
    }
    
    return real_ncclAllReduce(sendbuff, recvbuff, count, datatype,
                              op, actual_comm, stream);
}
```

## Shared Memory Handling

### CUDA IPC

```c
// lib/libnvsnap/src/checkpoint/cuda_ipc.c

typedef struct {
    CUdeviceptr local_ptr;
    CUipcMemHandle handle;
    size_t size;
    int source_pid;
    bool is_import;  // true if this process imported it
} CUDAIpcMapping;

static Vector* g_ipc_mappings = NULL;

void nvsnap_track_ipc_export(CUdeviceptr ptr, CUipcMemHandle* handle) {
    CUDAIpcMapping mapping = {
        .local_ptr = ptr,
        .handle = *handle,
        .size = get_allocation_size(ptr),
        .source_pid = getpid(),
        .is_import = false,
    };
    vector_push(g_ipc_mappings, &mapping);
}

void nvsnap_track_ipc_import(CUdeviceptr ptr, CUipcMemHandle* handle) {
    CUDAIpcMapping mapping = {
        .local_ptr = ptr,
        .handle = *handle,
        .size = 0,  // Unknown for imports
        .source_pid = 0, // Unknown
        .is_import = true,
    };
    vector_push(g_ipc_mappings, &mapping);
}

// During checkpoint:
// - Export mappings: checkpoint the memory (we own it)
// - Import mappings: don't checkpoint (other process owns it)

// During restore:
// - Export mappings: restore memory, get new handle, share with importers
// - Import mappings: wait for exporter to share new handle, re-import
```

### POSIX Shared Memory

```go
// internal/shm/tracker.go

type ShmRegion struct {
    Name      string  // /dev/shm/... name
    Size      int64
    OwnerPID  int
    Users     []int   // PIDs that have it mapped
    FD        int     // File descriptor in owner
}

func DiscoverSharedMemory(pids []int) ([]*ShmRegion, error) {
    regions := make(map[string]*ShmRegion)
    
    for _, pid := range pids {
        // Parse /proc/[pid]/maps for shm mappings
        maps, _ := parseProcMaps(pid)
        
        for _, m := range maps {
            if strings.HasPrefix(m.Path, "/dev/shm/") ||
               strings.HasPrefix(m.Path, "/SYSV") {
                name := m.Path
                if _, exists := regions[name]; !exists {
                    regions[name] = &ShmRegion{
                        Name:  name,
                        Size:  m.Size,
                        Users: []int{pid},
                    }
                } else {
                    regions[name].Users = append(regions[name].Users, pid)
                }
            }
        }
    }
    
    // Determine owner (usually first user or largest mapper)
    for _, r := range regions {
        r.OwnerPID = determineOwner(r)
    }
    
    return mapToSlice(regions), nil
}
```

## vLLM-Specific Handling

### Request Draining

```go
// internal/vllm/drain.go

type VLLMDrainer struct {
    enginePID int
    timeout   time.Duration
}

func (d *VLLMDrainer) DrainRequests(ctx context.Context) error {
    // Method 1: Set vLLM to drain mode via environment signal
    // vLLM checks NVSNAP_DRAIN_MODE file
    drainFile := fmt.Sprintf("/proc/%d/root/tmp/nvsnap_drain", d.enginePID)
    os.WriteFile(drainFile, []byte("1"), 0644)
    defer os.Remove(drainFile)
    
    // Method 2: Monitor request queue via /proc inspection
    // Look for Python objects or specific memory patterns
    
    // Method 3: Use libnvsnap to track scheduler state
    // (requires vLLM-specific knowledge)
    
    // Wait for queue to empty
    deadline := time.Now().Add(d.timeout)
    for time.Now().Before(deadline) {
        if d.isQueueEmpty() {
            return nil
        }
        time.Sleep(100 * time.Millisecond)
    }
    
    return fmt.Errorf("drain timeout")
}

func (d *VLLMDrainer) isQueueEmpty() bool {
    // Check libnvsnap state for NCCL activity
    // Check Python object state if accessible
    // Heuristic: no CUDA kernel launches for N ms
    return true
}
```

### KV Cache Optimization

```go
// internal/vllm/kvcache.go

// vLLM KV cache can be 10s of GB
// We optimize checkpointing with:
// 1. Only checkpoint allocated blocks (not free pool)
// 2. Incremental checkpointing (changed blocks only)
// 3. Parallel streaming

type KVCacheCheckpointer struct {
    blockSize     int64    // Size of each block
    numBlocks     int      // Total blocks
    allocatedMask []bool   // Which blocks are in use
}

func (k *KVCacheCheckpointer) CheckpointOptimized(ctx context.Context, w io.Writer) error {
    // Get block allocation map from libnvsnap
    // (vLLM's BlockAllocator state is tracked)
    
    // Only copy allocated blocks
    var totalBytes int64
    for i := 0; i < k.numBlocks; i++ {
        if k.allocatedMask[i] {
            blockPtr := k.basePtr + int64(i)*k.blockSize
            
            // Copy GPU → Host
            hostBuf := make([]byte, k.blockSize)
            cuMemcpyDtoH(hostBuf, blockPtr, k.blockSize)
            
            // Write block index + data
            binary.Write(w, binary.LittleEndian, int32(i))
            w.Write(hostBuf)
            
            totalBytes += k.blockSize
        }
    }
    
    return nil
}
```

## Testing Multi-Process Checkpoint

### Test Scenarios

```go
// test/integration/multi_process_test.go

func TestVLLMCheckpointRestore(t *testing.T) {
    // 1. Deploy vLLM with tensor parallelism
    deploy := &VLLMDeployment{
        Model:          "meta-llama/Llama-2-7b-chat-hf",
        TensorParallel: 2,
        GPUMemory:      "20Gi",
    }
    pod := deployVLLM(t, deploy)
    
    // 2. Send some inference requests
    responses := sendInferenceRequests(t, pod, 10)
    
    // 3. Checkpoint
    ckpt := createCheckpoint(t, pod)
    require.NotNil(t, ckpt)
    require.Equal(t, CheckpointPhaseCompleted, ckpt.Status.Phase)
    
    // 4. Delete original pod
    deletePod(t, pod)
    
    // 5. Restore to new pod
    restored := restoreCheckpoint(t, ckpt)
    require.NotNil(t, restored)
    
    // 6. Verify inference works
    newResponses := sendInferenceRequests(t, restored, 10)
    require.Len(t, newResponses, 10)
    
    // 7. Verify NCCL still works (multi-GPU communication)
    // Send request that requires all-reduce
    longResponse := sendLongInference(t, restored)
    require.NotEmpty(t, longResponse)
}

func TestNCCLReconstruction(t *testing.T) {
    // Test that NCCL communicators are properly reconstructed
    
    // 1. Deploy PyTorch DDP job with 4 GPUs
    job := deployDDPTraining(t, 4)
    
    // 2. Wait for training to start
    waitForTrainingStart(t, job)
    
    // 3. Checkpoint mid-training
    ckpt := createCheckpoint(t, job)
    
    // 4. Restore
    restored := restoreCheckpoint(t, ckpt)
    
    // 5. Verify all-reduce works
    // Check training continues with correct gradients
    verifyTrainingContinues(t, restored)
}
```

## Failure Handling

### Partial Checkpoint Failure

```go
func (c *CheckpointCoordinator) handlePartialFailure(
    ctx context.Context,
    completed []*ProcessCheckpoint,
    failed *Process,
    err error,
) error {
    c.logger.Error("partial checkpoint failure",
        zap.Int("completed", len(completed)),
        zap.Int("failed_pid", failed.PID),
        zap.Error(err))
    
    // Option 1: Abort and cleanup
    // Delete partial checkpoint data
    for _, pc := range completed {
        c.storage.Delete(pc.Path)
    }
    
    // Resume all processes
    c.unfreezeAll()
    c.sendToAll(ctx, MsgAbort{Reason: err.Error()})
    
    return fmt.Errorf("checkpoint aborted due to failure on PID %d: %w", 
                      failed.PID, err)
    
    // Option 2: Retry failed process (if idempotent)
    // Not recommended for GPU workloads due to state complexity
}
```

### Deadlock Prevention

```go
func (c *CheckpointCoordinator) waitForAll(ctx context.Context, msgType MsgType) error {
    received := make(map[int]bool)
    timeout := time.NewTimer(c.timeout)
    
    for len(received) < len(c.workload.Processes) {
        select {
        case <-ctx.Done():
            return ctx.Err()
            
        case <-timeout.C:
            // Log which processes haven't responded
            var missing []int
            for _, p := range c.workload.Processes {
                if !received[p.PID] {
                    missing = append(missing, p.PID)
                }
            }
            return fmt.Errorf("timeout waiting for %v", missing)
            
        case msg := <-c.msgChan:
            if msg.Type == msgType {
                received[msg.PID] = true
            }
        }
    }
    
    return nil
}
```

## Performance Considerations

### Parallel Checkpoint

```go
// Checkpoint multiple processes in parallel
// GPU memory copies can happen simultaneously

func (c *CheckpointCoordinator) checkpointParallel(
    ctx context.Context,
    procs []*Process,
    opts *CheckpointOptions,
) ([]*ProcessCheckpoint, error) {
    
    results := make([]*ProcessCheckpoint, len(procs))
    errChan := make(chan error, len(procs))
    
    // Limit parallelism to avoid overwhelming storage
    sem := make(chan struct{}, opts.MaxParallel)
    
    var wg sync.WaitGroup
    for i, proc := range procs {
        wg.Add(1)
        go func(idx int, p *Process) {
            defer wg.Done()
            
            sem <- struct{}{}        // Acquire
            defer func() { <-sem }() // Release
            
            result, err := c.checkpointProcess(ctx, p, opts)
            if err != nil {
                errChan <- fmt.Errorf("process %d: %w", p.PID, err)
                return
            }
            results[idx] = result
        }(i, proc)
    }
    
    wg.Wait()
    close(errChan)
    
    // Collect errors
    var errs []error
    for err := range errChan {
        errs = append(errs, err)
    }
    
    if len(errs) > 0 {
        return nil, fmt.Errorf("multiple failures: %v", errs)
    }
    
    return results, nil
}
```

## Summary

Multi-process checkpoint for vLLM requires:

1. **Process Discovery**: Find all related processes without container runtime
2. **Coordinated Protocol**: Barrier → Quiesce → Freeze → Checkpoint
3. **NCCL Reconstruction**: Save metadata, recreate communicators on restore
4. **Shared Memory Handling**: Track IPC mappings, coordinate restore
5. **vLLM-Specific**: Request draining, KV cache optimization
6. **Robust Error Handling**: Deadlock prevention, partial failure recovery

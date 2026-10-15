# Multi-GPU Checkpoint: NCCL Deadlock Analysis

## Problem Statement

Checkpointing a multi-GPU vLLM workload (TP=4, Llama-3.1-70B on 4x H100) fails
with `cuda-checkpoint timed out after 600s`. Single-GPU checkpoints work fine.

## Root Cause

CRIU's cuda plugin calls `cuda-checkpoint --action lock` on each GPU process
**sequentially**. For each process, it resumes a frozen "restore thread" via
`resume_restore_thread()` (ptrace CONT), which interacts with the NVIDIA driver
to lock GPU state.

The problem: NCCL collectives create **cross-GPU dependencies**. When GPU 0 is
locked, its NCCL ring buffers are frozen. When the plugin tries to lock GPU 2,
the resumed thread hits the NVIDIA driver which has pending NCCL operations
requiring GPU 0. Result: deadlock.

### Evidence from dump.log

```
(106s)  cuda_plugin: Checkpointing CUDA devices on pid 1327918 (GPU 0)
(111s)  cuda_plugin: cuda-checkpoint output ===> <=== (success, ~5s)
(114s)  cuda_plugin: Checkpointing CUDA devices on pid 1328379 (GPU 1)
(119s)  cuda_plugin: Checkpointing CUDA devices on pid 1328549 (GPU 2)
(719s)  Error: cuda-checkpoint timed out after 600s
```

GPU 0 locked fine. GPU 1 and 2 started but one hung for 600s waiting on the
now-locked GPU 0.

### Code Path

```
cr-dump.c:2257  checkpoint_devices()
  seize.c:1145    for_each_pstree_item → run_plugins(CHECKPOINT_DEVICES, pid)
    cuda_plugin.c:541   cuda_process_checkpoint_action(pid, "lock", timeout)
      cuda_plugin.c:472   resume_restore_thread(restore_tid)  ← unfreezes one thread
      cuda_plugin.c:117   launch_cuda_checkpoint()            ← forks cuda-checkpoint
      cuda_plugin.c:216   waitpid(child_pid)                  ← blocks until done
```

The sequential pattern: lock(GPU0) → wait → lock(GPU1) → wait → ...
means each GPU must complete before the next starts.

### Why Parallel Locking Won't Work

Forking all `cuda-checkpoint --action lock` calls simultaneously was considered
and rejected:

- **Fork does not give atomicity.** OS scheduling means one lock may start
  before another. No guarantee of simultaneous execution.
- **The deadlock proves cross-GPU dependency.** `--action lock` needs to drain
  NCCL operations, which requires cooperation from other GPUs. Parallel forks
  still deadlock — each lock tries to drain its side of the NCCL ring, but
  needs the other GPU to participate. Circular wait regardless of timing.
- **Unknown lock count.** The plugin discovers CUDA contexts at runtime — some
  PIDs have no CUDA context and are skipped.

The only viable fix is to eliminate NCCL cross-GPU dependencies **before** any
GPU locking begins.

---

## Approach A: Clean NCCL Destroy (Coordinated)

### Idea

Tear down NCCL communicators during the SIGUSR1 quiesce phase (before CRIU
freezes processes). All ranks coordinately call `ncclCommDestroy()`. With no
active communicators, each GPU process is independent and can be locked without
cross-GPU deadlocks. Recreate communicators on restore.

### How vLLM Uses NCCL

vLLM creates NCCL communicators via PyTorch's distributed module:

```
torch.distributed.init_process_group("nccl")  → ncclCommInitRank()
  ├── Tensor-parallel group (TP ranks)
  ├── Pipeline-parallel group (PP ranks)
  └── Data-parallel group (DP ranks)
```

Each communicator holds:
- Rank in group and world size
- ncclUniqueId (128-byte opaque blob, same across all ranks)
- GPU-GPU connection state (NVLink rings, PCIe topology, shared memory)
- Internal CUDA streams for async collectives

### Implementation: Three Components

#### 1. NCCL Interception in libnvsnap_intercept.so

Hook `ncclCommInitRank()` to track all communicators:

```c
// nccl_intercept.c (new file)
typedef struct {
    ncclComm_t comm;
    int rank;
    int nranks;
    ncclUniqueId unique_id;
} nvsnap_nccl_comm_t;

static nvsnap_nccl_comm_t g_nccl_comms[MAX_COMMS];
static int g_nccl_ncomms = 0;

// Hooked via LD_PRELOAD
ncclResult_t ncclCommInitRank(ncclComm_t *comm, int nranks,
                              ncclUniqueId id, int rank) {
    ncclResult_t rc = real_ncclCommInitRank(comm, nranks, id, rank);
    if (rc == ncclSuccess) {
        g_nccl_comms[g_nccl_ncomms++] = (nvsnap_nccl_comm_t){
            .comm = *comm, .rank = rank,
            .nranks = nranks, .unique_id = id
        };
    }
    return rc;
}
```

#### 2. Coordinated Destruction on SIGUSR1

`ncclCommDestroy()` is a **collective operation** — all ranks must call it
together or they deadlock. Requires a shared memory barrier.

```c
// On SIGUSR1 handler:
void nvsnap_quiesce_nccl(void) {
    // 1. Drain all pending GPU operations
    cudaDeviceSynchronize();

    // 2. Barrier: wait for all ranks to be ready
    //    (agent allocates /dev/shm/nvsnap-nccl-barrier before sending SIGUSR1)
    nvsnap_barrier_arrive_and_wait("/dev/shm/nvsnap-nccl-barrier", nranks);

    // 3. All ranks destroy communicators together
    for (int i = 0; i < g_nccl_ncomms; i++) {
        ncclCommDestroy(g_nccl_comms[i].comm);
    }

    // 4. Save metadata for restore
    nvsnap_save_nccl_metadata();
}
```

#### 3. NCCL Recreation on Restore

After CRIU restore, recreate communicators before the application resumes:

```c
void nvsnap_restore_nccl(void) {
    nvsnap_load_nccl_metadata();

    // Rank 0 generates new unique ID, broadcasts via shared memory
    ncclUniqueId new_id;
    if (rank == 0) ncclGetUniqueId(&new_id);
    nvsnap_broadcast_unique_id(&new_id);  // via /dev/shm

    // All ranks recreate with same rank/world_size
    for (int i = 0; i < g_nccl_ncomms; i++) {
        ncclCommInitRank(&g_nccl_comms[i].comm,
                         g_nccl_comms[i].nranks,
                         new_id,
                         g_nccl_comms[i].rank);
    }

    // Replace pointers in PyTorch's process group
    // (requires hooking into torch.distributed internals)
}
```

### Pros

- **Correct by construction** — no communicators = no cross-GPU dependencies
- **Clean state** — communicators are properly destroyed, no leaked resources
- **Enables cross-node restore** — new communicators can bind to different
  topology/hardware

### Cons

- **`ncclCommDestroy()` is collective** — requires a shared memory barrier so
  all ranks call it together. If one rank's signal handler fires before
  another's, the barrier must hold until all arrive.
- **Not async-signal-safe** — `ncclCommDestroy()` cannot be called directly
  from a signal handler. Must set a flag and have the main thread do the
  destruction, or use a dedicated thread.
- **PyTorch pointer replacement** — must replace communicator pointers inside
  PyTorch's `ProcessGroupNCCL`. Deep into PyTorch C++ internals, fragile
  across versions.
- **Multiple communicator groups** — vLLM may have TP, PP, DP communicators.
  Must track and restore all of them.
- **Estimated effort**: 2-4 weeks

### Open Questions

1. Can `ncclCommDestroy()` be called from a quiesce thread instead of a signal
   handler? We'd need the signal handler to wake a thread that does the actual
   work.
2. How does PyTorch's `ProcessGroupNCCL` react to its communicator being
   destroyed underneath it? May need to call
   `torch.distributed.destroy_process_group()` at the Python level.
3. What about NCCL's internal helper threads (proxy threads for inter-node
   communication)? Do they survive `ncclCommDestroy()`? Will CRIU capture
   them correctly?

---

## Approach B: NCCL Abort (Uncoordinated)

### Idea

Instead of cleanly destroying communicators, **abort** them. Each rank
independently calls `ncclCommAbort()` on SIGUSR1. Unlike `ncclCommDestroy()`,
abort is **NOT collective** — each process can call it on its own without
waiting for other ranks. After abort, the communicator is unusable but the GPU
has no pending NCCL operations. Then `cuda-checkpoint --action lock` proceeds
without cross-GPU dependencies. Recreate communicators on restore.

### Implementation

#### 1. NCCL Interception (same as Approach A)

Same `ncclCommInitRank()` hook to track communicators.

#### 2. Independent Abort on SIGUSR1

No barrier needed — each rank aborts its own communicators:

```c
void nvsnap_quiesce_nccl(void) {
    // 1. Drain all pending GPU operations
    cudaDeviceSynchronize();

    // 2. Abort all communicators (per-rank, no coordination)
    for (int i = 0; i < g_nccl_ncomms; i++) {
        ncclCommAbort(g_nccl_comms[i].comm);
    }

    // 3. Save metadata for restore
    nvsnap_save_nccl_metadata();
}
```

#### 3. NCCL Recreation on Restore (same as Approach A)

Same recreation logic — generate new unique ID, broadcast, `ncclCommInitRank()`.

### Pros

- **No coordination barrier for teardown** — each rank aborts independently.
  This is the key advantage over Approach A.
- **Simpler signal handler** — no shared memory barrier, no waiting for other
  ranks. Just abort and continue.
- **Faster to implement** — skip the hardest part of Approach A (the barrier).
- **Estimated effort**: 1-2 weeks

### Cons

- **`ncclCommAbort()` may leave GPU in a dirty state** — aborted communicators
  may leave NCCL internal resources (shared memory segments, GPU memory, proxy
  threads) in an undefined state. This could interfere with
  `cuda-checkpoint --action lock` or with CRIU's memory dump.
- **Still not async-signal-safe** — `ncclCommAbort()` is an NCCL library call,
  not safe to call from a signal handler. Same workaround needed (flag + thread).
- **PyTorch pointer replacement on restore** — same complexity as Approach A.
- **Abort semantics are less well-defined** — `ncclCommDestroy()` has clear
  semantics (clean teardown). `ncclCommAbort()` is designed for error recovery,
  not clean shutdown. May have edge cases.

### Open Questions

1. Does `ncclCommAbort()` actually release all GPU-side NCCL resources (ring
   buffers, shared memory mappings)? Or does it just mark the communicator as
   unusable and leave cleanup for later?
2. Will CRIU correctly capture the GPU state after an abort? The driver state
   may be in an error-recovery path that CRIU doesn't expect.
3. Does `ncclCommAbort()` kill NCCL proxy threads? If so, CRIU won't need to
   checkpoint them. If not, they may interfere.
4. Can we test this quickly by calling `ncclCommAbort()` from Python before
   triggering a checkpoint? (Would validate the approach without building the
   full C interception.)

---

## Comparison

| Aspect | A: Clean Destroy | B: Abort |
|--------|-------------------|----------|
| Teardown mechanism | `ncclCommDestroy()` | `ncclCommAbort()` |
| Coordination needed | Yes (shared memory barrier) | No (per-rank) |
| Teardown correctness | Clean, well-defined | Undefined edge cases |
| Lines of code | ~500-1000 | ~300-500 |
| Estimated effort | 2-4 weeks | 1-2 weeks |
| Restore complexity | Same | Same |
| Signal safety | Not async-signal-safe | Not async-signal-safe |
| Production quality | High (clean teardown) | Medium (abort semantics) |
| Cross-node restore | Yes | Yes |

Both approaches share the same restore path (NCCL interception + tracking +
recreation + PyTorch pointer replacement). The difference is only in teardown.

---

## Recommended Path

1. **Start with Approach B (Abort)** — faster to prototype, no barrier needed.
   Validate that `ncclCommAbort()` + `cuda-checkpoint --action lock` works
   without deadlock. Can test the abort semantics quickly from Python.

2. **If abort leaves dirty state**, switch to Approach A (Clean Destroy). Add
   the shared memory barrier for coordinated `ncclCommDestroy()`.

3. **Both approaches need**:
   - NCCL interception in libnvsnap_intercept.so (hook `ncclCommInitRank()`)
   - NCCL recreation on restore (new unique ID, `ncclCommInitRank()`)
   - PyTorch `ProcessGroupNCCL` pointer replacement
   - Async-signal-safe workaround (flag + thread, not direct call from handler)

### Quick Validation Test

Before building the full interception, test the abort approach from Python
inside a running TP=4 vLLM pod:

```python
import torch.distributed as dist

# Abort all NCCL process groups
for group in dist.distributed_c10d._world.pg_map.keys():
    if hasattr(group, '_get_backend'):
        backend = group._get_backend(torch.device('cuda'))
        if hasattr(backend, '_abort'):
            backend._abort()

# Then trigger checkpoint from agent
```

If the checkpoint succeeds after this, Approach B is validated.

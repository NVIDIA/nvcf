# Multi-GPU CUDA Restore via D2H/H2D Rehydration

**Status:** design — not yet implemented. Captures the architecture we'll
follow when we re-attempt multi-GPU GPU-state restore. Single-GPU CRIU
+ cuda-checkpoint already works; this doc is for the multi-GPU path
(currently blocked at the libcudart wall — see MEMORY.md).

**Related docs:**
- `docs/CUDA-INTERPOSITION-DESIGN.md` — the cuMemAlloc/Free interposition substrate WrapCore is built on.
- `docs/MULTI-GPU-PLAN.md` — older planning doc, superseded by this one for the GPU-state half.
- `docs/NCCL-MULTI-GPU-CHECKLIST.md` — operational checklist; this doc is the architecture.
- `docs/PHASE5B-CRIU-IN-WRITER-JOB.md` — the per-capture PVC speed work; orthogonal to multi-GPU GPU state.

## Goal

For multi-GPU inference workloads, do not attempt to restore NVIDIA UVM,
NCCL, CUDA IPC, CUDA Graphs, or opaque CUDA driver state directly.
Instead, checkpoint GPU data by copying device memory to host during
checkpoint, then restore by recreating GPU allocations and copying the
saved data back to device memory.

```
checkpoint: GPU memory  --D2H-->  checkpoint image
restore:    checkpoint image  --H2D-->  recreated GPU memory
```

This avoids depending on fragile multi-GPU UVM/NVSwitch state and makes
GPU state reconstructable at the platform layer.

## Core Restore Model

Restore should follow this order:

1. Restore CPU process state with CRIU.
2. Reinitialize CUDA after restore.
3. Bind each restore thread to the correct GPU/context.
4. Recreate per-GPU allocations.
5. Restore allocation contents with H2D.
6. Rebuild or patch pointer references.
7. Recreate disposable CUDA objects:
   - streams
   - events
   - cuBLAS/cuDNN handles
   - NCCL communicators
   - CUDA IPC handles
   - CUDA Graphs
8. Resume serving.

The key rule is:

> **Checkpoint data, not CUDA driver objects.**

CUDA driver state is not portable across restore.

## Required Allocation Metadata

Each tracked allocation needs a manifest record:

```c
struct GpuAllocRecord {
    int      logical_gpu_id;
    char     gpu_uuid[64];
    uint64_t old_device_ptr;
    size_t   size;
    size_t   alignment;
    uint32_t alloc_type;
    char     name[128];
    off_t    checkpoint_offset;
};
```

This is effectively the metadata already captured by the WrapCore
allocation tracker (`$HOME/personal/metabalite/WrapCore`):
`logical_gpu_id`, `gpu_uuid`, `old_device_ptr`, `size`, `alloc_type`,
`name`, `checkpoint_offset`.

This metadata is enough to restore raw allocation contents, but not
always enough to repair every reference to those allocations.

## The CUDA-Error-400 Footgun

A common restore failure is:

```
cudaErrorInvalidResourceHandle = 400
```

In multi-GPU restore, this usually means a CUDA object was created
under one device/context and reused under another.

**Bad pattern:**

```c
cudaSetDevice(0);
cudaStreamCreate(&stream);
cudaSetDevice(1);
cudaMemcpyAsync(dst_on_gpu1, src, size, cudaMemcpyHostToDevice, stream);
```

**Correct pattern:**

```c
cudaSetDevice(1);
cudaStreamCreateWithFlags(&stream_gpu1, cudaStreamNonBlocking);
cudaMemcpyAsync(dst_on_gpu1, src, size, cudaMemcpyHostToDevice, stream_gpu1);
```

Every CUDA object must be recreated after the correct device is current:

```
cudaSetDevice(gpu)
  cudaMalloc(...)
  cudaStreamCreate(...)
  cudaEventCreate(...)
  cublasCreate(...)
  cudnnCreate(...)
  ncclCommInitRank(...)
```

Do not reuse pre-checkpoint streams, events, library handles, CUDA Graph
execs, NCCL communicators, or IPC handles.

## Two Restore Patterns

### Option A: Same-VA Restore (preferred)

Recreate allocations at the original device virtual addresses using CUDA
virtual memory APIs:

- `cuMemAddressReserve`
- `cuMemCreate`
- `cuMemMap`
- `cuMemSetAccess`
- `cuMemcpyHtoD`

This is the cleanest option because existing device pointers remain valid.

However, this depends on having a clean GPU VA space. In same-process
experiments, `cuMemAddressReserve` with an address hint can become
unreliable after prior `cuMemFree` activity and range coalescing. After
CRIU restore, the process/device state is effectively fresh, so same-VA
restore is more viable there than in same-process teardown/rebuild
gymnastics.

> **Important constraint:** same-VA restore is attractive specifically
> because CRIU gives us a clean post-restore process state. It should
> not be assumed reliable inside an already-mutated CUDA process.

### Option B: Allocate Fresh + Remap

Allocate new device memory and maintain a remap table:

```
old_device_ptr_range -> new_device_ptr_range
```

This is operationally more flexible but requires pointer repair. It
works well when old device pointers live in CPU-visible metadata:

- tensor metadata
- allocator records
- kernel argument blocks
- runtime bookkeeping

It does **not** automatically work when old device pointers are embedded
inside another GPU allocation.

## GPU-Resident Pointer Problem

Pointer remapping is not just a CPU metadata problem.

If a device pointer is stored inside another GPU buffer — e.g. GPU
allocation A contains a pointer to GPU allocation B — then a CPU-side
remap table is insufficient.

This is common in framework/runtime structures:

- vLLM KV cache index buffers
- indirection tables
- device-side pointer arrays
- custom kernel metadata

To repair this case, restore must perform:

1. D2H referencing buffer
2. rewrite embedded `old_device_ptr` values
3. H2D referencing buffer back

So the restore system needs to classify allocations as either:

- raw data buffer
- CPU-patchable metadata
- GPU-resident pointer-containing buffer
- opaque framework/runtime allocation

Same-VA restore avoids much of this problem. Allocate-fresh restore must
explicitly handle GPU-resident pointer rewriting.

## NCCL Recreate Is an Orchestration Problem

"Rebuild NCCL communicators" is not a local operation.

For vLLM/SGLang-style multi-process inference, `ncclCommInitRank`
requires all ranks to participate with a shared `ncclUniqueId`. After
restore, workers may not have a valid synchronization channel or
rendezvous mechanism.

A real implementation needs an external coordinator. Per implementation
note (see "Implementation Guardrails" below): **nvsnap-server is the
orchestrator; nvsnap-agent is the per-node executor.**

The orchestrator must coordinate:

- rank membership
- GPU/rank mapping
- `ncclUniqueId` generation and exchange
- barrier before `ncclCommInitRank`
- failure handling if one rank restore fails

Without this, restored workers can individually restore memory but still
fail to reconstruct distributed execution.

## Framework Allocator State Is Not Free

Framework allocators are a major restore boundary.

For PyTorch, `THCCachingAllocator` owns internal device pointers, block
metadata, pools, and stream associations. If we allocate fresh and remap
pointers, PyTorch's private allocator state may still point at old
addresses.

Possible approaches:

1. Same-VA restore so allocator metadata remains valid.
2. Restore before framework allocator is initialized.
3. Fork/patch framework allocator internals.
4. Treat framework CUDA state as disposable and rebuild the model/runtime.

The cleanest implementation path is usually same-VA restore or restoring
at a point before framework CUDA state becomes deeply initialized.
Allocate-fresh + remap is hard if the framework owns opaque private
pointer state.

## CUDA Graphs Cost

CUDA Graphs should be treated as disposable. They cannot be safely
restored as opaque executable driver state. For vLLM 0.11+ and similar
serving stacks, CUDA graphs are used on hot paths such as prefill.
Recreating them generally means rerunning the warmup/capture path.

Operational impact:

```
CUDA Graph restore = rebuild + warmup cost
```

For vLLM-class workloads, this can be on the order of tens of seconds
(~30 seconds for an 8B model warmup path).

So the restore design should explicitly budget for:

- memory H2D time
- runtime reinitialization time
- NCCL rendezvous time
- CUDA Graph recapture/warmup time

---

## Implementation Guardrails

### 1. GPU-Resident Pointer Detection Requires Allocation-Time Labels

Pointer-containing GPU buffers cannot be discovered reliably after the
fact. At runtime, an embedded device pointer inside GPU memory is just
an 8-byte value. Without out-of-band metadata, restore cannot know
whether a word-sized value is a real device pointer, an integer, a
packed field, a hash, an offset, or opaque model/runtime data.

Therefore, allocation classification must happen at allocation time.

**Recommended API:**

```c
typedef enum {
    NVSNAP_KIND_RAW_DATA,
    NVSNAP_KIND_CPU_PATCHABLE_METADATA,
    NVSNAP_KIND_GPU_POINTER_TABLE,
    NVSNAP_KIND_OPAQUE
} nvsnap_alloc_kind_t;

void* nvsnap_alloc_with_kind(size_t size, nvsnap_alloc_kind_t kind);
```

Restore behavior per kind:

- `NVSNAP_KIND_RAW_DATA`: restore bytes directly.
- `NVSNAP_KIND_CPU_PATCHABLE_METADATA`: patch CPU-side references before resume.
- `NVSNAP_KIND_GPU_POINTER_TABLE`: D2H buffer, rewrite embedded `old_device_ptr` values, H2D buffer back.
- `NVSNAP_KIND_OPAQUE`: require same-VA restore. Do not attempt allocate-fresh remap.

**Default policy must be conservative:** unknown buffer kind → opaque.
Unknown GPU buffers should not be interpreted or scanned for
pointer-looking values.

**Phased deployment plan** (the API is easy; getting it called is hard):

- **v1:** every interposed `cudaMalloc`/`cuMemAlloc` defaults to
  `NVSNAP_KIND_OPAQUE`. Same-VA required for everything. No framework
  changes needed; pure LD_PRELOAD interception.
- **v2:** per-framework patches that call `nvsnap_alloc_with_kind`
  explicitly for known pointer-table buffers (e.g., vLLM KV cache index
  buffer, PyTorch CUDA tensor metadata blocks).
- **v3:** static analysis or annotated kernel headers for upstream-friendly labels.

Without this phasing, the API ships and is unused for months.

### 2. Same-VA Restore Must Fail Fast

Same-VA restore is the preferred path because it preserves:

- framework allocator metadata
- GPU-resident embedded pointers
- kernel argument assumptions
- CUDA graph capture assumptions
- opaque runtime state

If any allocation cannot be restored at its original device virtual
address, **restore must abort**.

**Policy:**

```
same-VA success for all tracked allocations:
  continue restore

same-VA failure for any required allocation:
  abort restore
  tear down partial CUDA state
  fall back to cold start
```

Do not silently degrade from same-VA to allocate-fresh + remap.

Implementation: do a "dry run" reservation pass first. Try
`cuMemAddressReserve(hint=old_va, …)` for every tracked allocation
before mapping any pages. If any reservation fails, release the reserved
ranges and abort the entire restore.

For vLLM/SGLang/PyTorch-style inference stacks, the proof that all
references are patchable usually does not hold — same-VA failure is
terminal.

> **The most important invariant in this doc:** same-VA restore failure
> is terminal for the restore attempt; it must not silently fall back
> to allocate-fresh remap unless the workload has explicitly proven all
> pointer references are patchable.

### 3. GPU UUID to Ordinal Mapping Is Required

The checkpoint manifest keys allocations by stable GPU identity (UUID),
not by runtime ordinal. But restore APIs like `cudaSetDevice()` take
ordinals, and ordinals can change due to:

- PCI re-enumeration
- `CUDA_VISIBLE_DEVICES` changes
- MIG reconfiguration
- GPU offline / replacement
- cross-node restore
- scheduler placement differences

Restore must perform an explicit lookup pass:

```c
for (int ordinal = 0; ordinal < device_count; ordinal++) {
    cudaSetDevice(ordinal);
    cudaDeviceGetUuid(&uuid, ordinal);
    uuid_to_ordinal[uuid] = ordinal;
}

for each checkpoint_gpu in manifest {
    if (!uuid_to_ordinal.contains(checkpoint_gpu.uuid)) {
        abort_restore("required GPU UUID not present");
    }
    checkpoint_gpu.current_ordinal = uuid_to_ordinal[checkpoint_gpu.uuid];
}
```

For cross-node restore, this becomes a placement constraint: the
restore target must provide equivalent GPU UUIDs/topology, OR restore
must intentionally support logical remapping. If logical remapping is
unsupported, missing UUIDs trigger cold start.

NCCL communicators are bound by *rank*, not UUID. After the
UUID→ordinal map is built, the orchestrator must also decide which UUID
maps to rank 0 on the target node. Default policy: preserve the original
UUID→rank mapping if all UUIDs are present; otherwise cold-start.

### 4. NCCL Restore Needs Timeout-and-Abort Semantics

NCCL communicator recreation is a distributed operation, not a local
restore step. If only some ranks restore successfully, the surviving
ranks can hang forever in `ncclCommInitRank`, process-group init, or
framework distributed barriers.

**Orchestrator placement:**

- **nvsnap-server:** central orchestrator. Owns the state machine, tracks
  per-rank readiness, broadcasts `ncclUniqueId`, drives timeout/abort.
- **nvsnap-agent:** per-node executor. Reports ranks' readiness up,
  receives commands down, kills workers on abort.

(`nvsnap-agent` alone cannot orchestrate cross-node restore — it's
per-node. `nvsnap-server` is the natural cluster-wide coordinator.)

**Required policy:**

```
all ranks reach rendezvous before timeout:
  exchange ncclUniqueId
  recreate communicators
  continue restore

any rank fails or times out:
  abort NCCL init on all ranks
  tear down partial restore
  retry full group or cold start
```

Do not allow partial groups to block indefinitely.

**State machine:**

```
RESTORE_PREPARE
  -> WAIT_FOR_ALL_RANKS
  -> EXCHANGE_NCCL_UNIQUE_ID
  -> NCCL_INIT_BARRIER
  -> RESTORE_COMMIT
  -> SERVING

on timeout/failure:
  -> RESTORE_ABORT
  -> COLD_START_OR_RETRY
```

**Abort primitive caveat:** `ncclCommAbort` is itself unreliable on a
hung communicator (NCCL upstream issue #829, see MEMORY.md). The
"abort NCCL init on all ranks" transition can hang waiting on the abort
itself. The real abort primitive is **SIGKILL the worker processes** —
nvsnap-agent has hostPID + privileged so it can. Release whatever
GPU/NCCL state by terminating workers; the orchestrator then decides
cold-start vs retry.

### 5. Kernel Argument Blocks Are Patchable Only Under Controlled Relaunch

CUTLASS, Triton, and framework-generated kernels often use CPU-side
argument blocks containing device pointers. These are typically
marshalled at `cuLaunchKernel` time.

These pointers are CPU-patchable **only** if the framework will
recreate or relaunch kernels using fresh argument blocks after restore.

**Safe case:**

- framework allocator/runtime is recreated
- kernel launches happen after pointer remap
- new arg blocks are generated from current tensor metadata

**Unsafe case:**

- old CPU arg blocks survive CRIU restore
- old device pointers remain inside them
- framework reuses stale launch metadata
- CUDA graphs reuse captured old pointers

This connects directly to the same-VA policy:

- If same-VA restore succeeds: old kernel arg pointers remain valid.
- If allocate-fresh restore is used: arg blocks must be patched or
  regenerated before any launch.

For CUDA Graphs, assume captured graph state is disposable: destroy and
recreate, rerun warmup/capture, do not reuse old `cudaGraphExec`
objects.

---

## Production Restore Policy (summary)

1. Use same-VA restore as the default and preferred path.
2. Require allocation-time buffer classification; default unknown to
   `NVSNAP_KIND_OPAQUE`.
3. Abort restore if same-VA fails for any required allocation.
4. Build GPU UUID → current ordinal mapping before restore.
5. Coordinate NCCL through nvsnap-server (orchestrator) + nvsnap-agent
   (per-node executor) with timeout-and-abort semantics. SIGKILL is
   the abort primitive, not `ncclCommAbort`.
6. Recreate CUDA Graphs and framework runtime objects.
7. Fall back to cold start rather than silently switching to unsafe
   remap.

## Production Risks (track these during implementation)

1. `cudaErrorInvalidResourceHandle` from wrong device/context ownership.
2. GPU-resident embedded pointers that cannot be patched from CPU
   metadata alone.
3. NCCL communicator recreation requiring cross-worker coordination.
4. Framework allocator internals retaining old pointer state.
5. CUDA Graph recapture/warmup adding non-trivial restore latency.
6. Same-VA restore only being reliable in a clean post-CRIU process state.

## Implementation Stance

- Use D2H/H2D for data.
- Use same-VA restore where possible.
- Rebuild CUDA/NCCL objects.
- Coordinate distributed restore externally (nvsnap-server).
- Do not assume pointer remapping is globally sufficient.

## Bottom Line

The reliable architecture is:

- Checkpoint GPU allocation contents.
- Restore into a clean post-CRIU CUDA process.
- Prefer original GPU virtual addresses.
- Recreate all disposable CUDA/NCCL/framework runtime objects.
- Use nvsnap-server as the orchestrator for multi-rank NCCL rejoin.

---

## Deployment and Operations Notes

### 1. Allocation-Kind API Requires a Phased Rollout

The `nvsnap_alloc_with_kind()` API is the right long-term interface, but
allocation kind cannot be inferred reliably from a generic CUDA
allocation interceptor.

An LD_PRELOAD layer can intercept:

- `cudaMalloc`
- `cuMemAlloc`
- `cuMemCreate`

but it cannot know whether the allocation is:

- weight tensor
- KV-cache data buffer
- KV-cache index buffer
- GPU pointer table
- framework allocator slab
- opaque runtime metadata

Only the framework or allocator owner has that semantic knowledge.
Therefore, rollout should be phased.

#### v1: Conservative default

All interposed allocations default to:

```
NVSNAP_KIND_OPAQUE
```

Policy:

```
unknown allocation kind = opaque
opaque allocation       = same-VA required
same-VA failure         = restore abort / cold start
```

This requires no framework changes and is the safest initial deployment.

#### v2: Framework-specific annotations

Add targeted framework patches for known allocation types:

```c
nvsnap_alloc_with_kind(size, NVSNAP_KIND_RAW_DATA);
nvsnap_alloc_with_kind(size, NVSNAP_KIND_GPU_POINTER_TABLE);
nvsnap_alloc_with_kind(size, NVSNAP_KIND_CPU_PATCHABLE_METADATA);
nvsnap_alloc_with_kind(size, NVSNAP_KIND_OPAQUE);
```

Likely targets:

- vLLM KV-cache index buffers
- vLLM block tables
- SGLang runtime buffers
- PyTorch allocator-owned slabs
- custom tensor-parallel metadata buffers

This is where allocate-fresh + remap becomes viable for specific,
proven-safe allocation classes.

#### v3: Upstream-friendly labels

Longer term, support static or declarative annotations:

- annotated kernel headers
- framework allocation tags
- allocator metadata extensions
- compiler/runtime-generated buffer descriptors

The goal is to make allocation classification explicit without carrying
a large private framework fork.

**Bottom-line policy:**

- Do not depend on automatic pointer detection.
- Start opaque + same-VA.
- Add explicit allocation labels only where the framework can prove
  semantics.

### 2. NCCL Rendezvous Belongs in nvsnap-server, Not nvsnap-agent

NCCL communicator recreation is a cluster-wide coordination problem.
A per-node nvsnap-agent is not sufficient by itself for multi-node,
multi-rank restore.

The split should be:

```
nvsnap-server:
  central restore orchestrator

nvsnap-agent:
  per-node privileged executor
```

#### nvsnap-server responsibilities

- own restore state machine
- track rank membership
- track per-rank readiness
- validate GPU/rank placement
- generate or receive `ncclUniqueId`
- broadcast `ncclUniqueId` to ranks
- enforce restore timeout
- decide retry vs cold start
- issue abort commands
- commit restore only when the full rank set is ready

State machine:

```
RESTORE_PREPARE
  -> WAIT_FOR_ALL_RANKS
  -> EXCHANGE_NCCL_UNIQUE_ID
  -> NCCL_INIT_BARRIER
  -> RESTORE_COMMIT
  -> SERVING
```

Failure path:

```
timeout / rank failure / placement mismatch
  -> RESTORE_ABORT
  -> COLD_START_OR_RETRY
```

#### nvsnap-agent responsibilities

- restore local workers
- report local rank readiness to nvsnap-server
- receive `ncclUniqueId` / restore commands
- inject environment/config into local workers
- kill local workers on abort
- clean up node-local state

This avoids requiring agent-to-agent gossip and keeps the global
restore decision in one place.

### 3. NCCL Abort Means Kill the Worker, Not Trust ncclCommAbort

On timeout or partial restore failure, the system should not rely on
`ncclCommAbort()` as the primary recovery mechanism.

In corrupted or half-restored distributed CUDA/NCCL state, even abort
APIs can hang or fail to make forward progress. The reliable abort
primitive is process termination.

**Operational policy:**

```
NCCL restore timeout       = kill affected worker processes
partial rank restore       = kill the whole rank set
corrupted NCCL state       = kill workers, do not attempt in-process cleanup
```

The actual abort path:

```
nvsnap-server detects timeout/failure
  -> nvsnap-server sends abort to all relevant nvsnap-agents
  -> nvsnap-agents SIGKILL worker processes
  -> GPU/NCCL/CUDA state is released by process teardown
  -> nvsnap-server decides retry full restore or cold start
```

So the failure state machine:

```
RESTORE_ABORT:
  terminate workers via nvsnap-agent SIGKILL
  do not depend on ncclCommAbort
```

This is especially important for multi-rank restore, where allowing
4 of 8 ranks to hang inside NCCL can wedge the entire recovery path.

### Final Policy Additions

Add these rules to the implementation policy:

1. **Allocation kind rollout is phased:**
   v1 opaque-by-default + same-VA required,
   v2 framework-specific annotations,
   v3 upstream/static labels.

2. **nvsnap-server owns cluster-wide restore orchestration.**
   nvsnap-agent only executes node-local restore/kill/cleanup commands.

3. **NCCL timeout/abort recovery uses SIGKILL of worker processes.**
   Do not rely on `ncclCommAbort` for corrupted or partial restore
   state.

The crisp production stance:

- Unknown allocation semantics require same-VA.
- Same-VA failure aborts restore.
- Cluster-wide NCCL rejoin is coordinated by nvsnap-server.
- Failed NCCL restore is recovered by killing workers, not by trusting
  NCCL cleanup.

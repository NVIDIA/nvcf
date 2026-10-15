# NCCL Quiesce: File-Based Trigger + Shared Memory Barrier

## Problem

Multi-GPU vLLM (TP=4, Llama-3.1-70B) uses NCCL for cross-GPU communication.
CRIU's cuda plugin calls `cuda-checkpoint --action lock` on each GPU process
sequentially. Locking GPU 0 freezes its NCCL ring buffers, causing GPU 1-3 to
deadlock on pending NCCL operations.

Signal-based quiesce (SIGUSR1) doesn't work reliably in worker processes —
PyTorch/NCCL overrides our signal handler after installation.

## Solution

File-based trigger + shared memory barrier. No signals needed.

### Architecture

```
Agent                         Worker Processes (rank 0-3)
  |                               |
  |  1. Create trigger file       | quiesce thread polling (1ms)
  |  /dev/shm/nvsnap-quiesce      |
  |                               |
  |                           2. Each worker sees trigger file
  |                           3. cudaDeviceSynchronize()
  |                           4. Atomic increment barrier counter
  |                           5. Spin until counter == nranks
  |                           6. ncclCommAbort() on all comms
  |                           7. Write per-rank done marker
  |                               |
  |  8. Agent polls for all       |
  |     done markers              |
  |                               |
  |  9. Proceed with CRIU dump    |
  |                               |
  | 10. Remove trigger file       |
  |     after checkpoint          |
```

### Shared Memory Layout

```
/dev/shm/nvsnap-quiesce          <- trigger file (created by agent)
  Contents: nranks (e.g., "4")  <- expected number of GPU ranks

/dev/shm/nvsnap-quiesce-barrier  <- mmap'd shared memory (created by first worker)
  Offset 0:  atomic_int ready_count    <- incremented after cudaDeviceSynchronize
  Offset 4:  atomic_int abort_count    <- incremented after ncclCommAbort
  Offset 8:  int nranks                <- expected count (from trigger file)

/dev/shm/nvsnap-quiesce-done-<pid>  <- per-process done marker (created by worker)
  Contents: "done"
```

### Sequence of Operations

#### Agent Side (checkpoint.go)

```
1. Stop readiness probe (optional — prevents new requests during checkpoint)
2. Write nranks to /dev/shm/nvsnap-quiesce (via kubectl exec or /proc/PID/root)
3. Poll for /dev/shm/nvsnap-quiesce-done-<pid> for each worker PID
   - Timeout: 30 seconds
4. All workers done → proceed with CRIU dump
5. After CRIU dump: remove trigger + barrier + done files
```

#### Worker Side (quiesce thread in nccl_intercept.c)

```
1. Quiesce thread polls for /dev/shm/nvsnap-quiesce every 100ms
2. When trigger file found:
   a. Read nranks from file
   b. Open/create /dev/shm/nvsnap-quiesce-barrier (mmap shared)
   c. Store nranks in barrier
   d. Call cudaDeviceSynchronize() — drain pending GPU ops
   e. Atomic increment ready_count
   f. Spin until ready_count == nranks (all ranks synchronized)
   g. Call ncclCommAbort() on all tracked communicators
   h. Atomic increment abort_count
   i. Write /dev/shm/nvsnap-quiesce-done-<pid>
   j. Spin until trigger file is removed (checkpoint complete)
```

### Why This Works

1. **No signals needed** — polling avoids the PyTorch/NCCL signal handler
   conflict entirely.

2. **Barrier ensures safety** — all ranks complete `cudaDeviceSynchronize()`
   before any rank calls `ncclCommAbort()`. This prevents aborting one rank
   while another has in-flight NCCL operations.

3. **Works with active inference** — `cudaDeviceSynchronize()` waits for
   pending GPU operations (including NCCL collectives) to complete. The
   barrier then ensures all ranks are idle before aborting.

4. **Deterministic** — no timing-dependent races. The barrier is a hard
   synchronization point.

### Failure Modes

| Scenario | Handling |
|----------|----------|
| Worker dies before reaching barrier | Agent timeout (30s) → checkpoint fails |
| cudaDeviceSynchronize hangs (stuck NCCL op) | Agent timeout → checkpoint fails |
| One rank faster than others | Barrier spin-waits (busy loop, ~1ms polls) |
| Trigger file exists from previous failed checkpoint | Agent removes stale files before creating new trigger |
| nranks mismatch | Workers read nranks from trigger file — agent sets it from process discovery |

### Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `NVSNAP_NCCL_INTERCEPT` | 0 | Enable NCCL interception |
| `NVSNAP_NCCL_QUIESCE_POLL_MS` | 100 | Trigger file poll interval (ms) |
| `NVSNAP_NCCL_BARRIER_TIMEOUT_S` | 30 | Barrier wait timeout (seconds) |

### Files Modified

| File | Change |
|------|--------|
| `lib/nvsnap_intercept/src/nccl_intercept.c` | Add quiesce thread polling + barrier logic |
| `lib/nvsnap_intercept/src/quiesce.c` | Remove signal-based NCCL quiesce call |
| `internal/agent/checkpoint.go` | Create trigger file, poll for done markers |

### Comparison with Signal-Based Approach

| Aspect | Signal (SIGUSR1) | File + Barrier |
|--------|------------------|----------------|
| Reliability | Fragile — handler overridden by PyTorch/NCCL | Robust — no handler conflicts |
| Coordination | None (each rank acts independently) | Barrier ensures all ranks sync |
| Active inference | Unsafe (abort during in-flight op) | Safe (cudaDeviceSync drains first) |
| Latency | Instant signal delivery | 100ms polling + barrier wait |
| Complexity | Simple handler, complex debugging | More code, but deterministic |
| Production-ready | No | Yes |

### Phase 2: Restore-side NCCL Recreation

Not in this change. After checkpoint + restore:
- Workers have aborted communicators
- Need to recreate with new ncclUniqueId
- Requires intercepting NCCL collective calls to lazy-recreate
- Separate design doc when ready

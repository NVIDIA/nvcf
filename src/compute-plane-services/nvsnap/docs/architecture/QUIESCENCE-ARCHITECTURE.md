# NVSNAP Quiescence Architecture

## Overview

NVSNAP (GPU Checkpoint/Restore) enables transparent checkpoint and restore of GPU-accelerated containers in Kubernetes. A key challenge is handling asynchronous I/O subsystems (io_uring, libuv/uvloop) that maintain kernel-side state incompatible with CRIU's checkpoint mechanism.

This document describes the **generic quiescence architecture** that solves these problems without requiring application modifications.

## The Problem

### io_uring Challenges

io_uring is Linux's high-performance async I/O interface. It presents several challenges for checkpoint/restore:

1. **Pending I/O**: Submission queue (SQ) and completion queue (CQ) may have in-flight operations
2. **SQPOLL Kernel Thread**: When `IORING_SETUP_SQPOLL` is enabled, a kernel polling thread (`iou-sqp-<pid>`) actively accesses the ring
3. **Registered Resources**: Files and buffers registered with io_uring have kernel-side state

```
┌─────────────────────────────────────────────────────────────────┐
│                    io_uring Architecture                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  User Space              │  Kernel Space                        │
│  ───────────────────────────────────────────────────────────    │
│                          │                                       │
│  ┌──────────────────┐    │    ┌──────────────────┐              │
│  │  Application     │    │    │  io_uring        │              │
│  │                  │    │    │  Instance        │              │
│  │  submit_sqe() ───────────► │  ┌────────────┐  │              │
│  │                  │    │    │  │ SQ Ring    │  │              │
│  │  poll_cqe() ◄────────────── │  │ sq_head=5 │  │              │
│  │                  │    │    │  │ sq_tail=8 │◄───── 3 pending  │
│  └──────────────────┘    │    │  └────────────┘  │              │
│                          │    │                  │              │
│                          │    │  ┌────────────┐  │              │
│                          │    │  │ CQ Ring    │  │              │
│                          │    │  │ cq_head=2 │  │              │
│                          │    │  │ cq_tail=2 │◄───── 0 pending  │
│                          │    │  └────────────┘  │              │
│                          │    │                  │              │
│                          │    │  ┌────────────┐  │              │
│                          │    │  │ SQPOLL    │  │◄── Kernel    │
│                          │    │  │ Thread    │  │    thread     │
│                          │    │  └────────────┘  │              │
│                          │    └──────────────────┘              │
└─────────────────────────────────────────────────────────────────┘
```

**CRIU cannot checkpoint when:**
- SQ has pending submissions (`sq_head != sq_tail`)
- CQ has unreaped completions (`cq_head != cq_tail`)
- SQPOLL thread is actively polling

### libuv/uvloop Challenges

uvloop is a fast Python event loop built on libuv. After CRIU restore:

1. **Stale Backend FDs**: libuv's epoll/kqueue FD is invalid after restore
2. **Cached Handle Pointers**: uvloop's Python objects contain C pointers to `uv_handle_t` structures
3. **Signal State**: Signal handler registrations are lost

```
┌─────────────────────────────────────────────────────────────────┐
│                   uvloop/libuv State                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Python Layer (uvloop)        │  C Layer (libuv)                │
│  ─────────────────────────────────────────────────────────────  │
│                               │                                  │
│  ┌─────────────────────────┐  │  ┌─────────────────────────┐    │
│  │ Loop (Python object)    │  │  │ uv_loop_t              │    │
│  │                         │  │  │                         │    │
│  │ self._loop ─────────────────► │ backend_fd = 7 ◄──────────── │
│  │                         │  │  │ time = 1234567          │    │ Stale after
│  └─────────────────────────┘  │  │ active_handles = [...]  │    │ CRIU restore
│                               │  └─────────────────────────┘    │
│  ┌─────────────────────────┐  │                                  │
│  │ UVProcess               │  │  ┌─────────────────────────┐    │
│  │                         │  │  │ uv_process_t            │    │
│  │ self._handle ───────────────► │ pid = 1234              │    │
│  │                         │  │  │ exit_cb = 0x7fff...     │◄──── Invalid
│  └─────────────────────────┘  │  └─────────────────────────┘    │ pointer
│                               │                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Solution Architecture

### Design Principles

1. **Generic**: Works with any container, no application changes required
2. **Transparent**: Uses LD_PRELOAD, invisible to applications
3. **Safe**: No-op if library isn't loaded or signals aren't sent
4. **Cooperative**: Application continues running during tracking, only pauses for checkpoint

### Component Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         NVSNAP Quiescence System                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                     libnvsnap_intercept.so (LD_PRELOAD)                   ││
│  │  ┌───────────────────┐ ┌───────────────────┐ ┌───────────────────────┐  ││
│  │  │ io_uring_intercept│ │ libuv_intercept   │ │ quiesce coordinator   │  ││
│  │  │                   │ │                   │ │                       │  ││
│  │  │ • syscall() hook  │ │ • uv_loop_init()  │ │ • SIGUSR1 handler     │  ││
│  │  │ • io_uring_setup  │ │ • uv_run()        │ │ • SIGUSR2 handler     │  ││
│  │  │ • io_uring_enter  │ │ • uv_*_init()     │ │ • drain_io_uring()    │  ││
│  │  │                   │ │ • uv_spawn()      │ │ • reinit_libuv()      │  ││
│  │  │ Tracks: ring FDs, │ │ Tracks: loop ptrs │ │                       │  ││
│  │  │ entries, flags    │ │ handle ptrs       │ │ State: NONE/REQUESTED/│  ││
│  │  │                   │ │                   │ │        COMPLETE/RESUME│  ││
│  │  └───────────────────┘ └───────────────────┘ └───────────────────────┘  ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                           NVSNAP Agent                                    ││
│  │  ┌───────────────────────────────────────────────────────────────────┐  ││
│  │  │                      Checkpoint Flow                               │  ││
│  │  │                                                                    │  ││
│  │  │  1. cuda-checkpoint --lock (freeze GPU)                           │  ││
│  │  │  2. syscall.Kill(pid, SIGUSR1)  ──► trigger quiescence            │  ││
│  │  │  3. sleep(2s) for drain completion                                │  ││
│  │  │  4. CRIU dump                                                     │  ││
│  │  │  5. cuda-checkpoint --unlock                                      │  ││
│  │  │  6. syscall.Kill(pid, SIGUSR2)  ──► resume (if leave-running)     │  ││
│  │  └───────────────────────────────────────────────────────────────────┘  ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Checkpoint Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Checkpoint Timeline                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  Container Process                    NVSNAP Agent                            │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  Running normally...                                                         │
│  io_uring: 3 pending ops                                                     │
│  libuv: loop active                                                          │
│       │                                                                      │
│       │                               Checkpoint request received            │
│       │                                      │                               │
│       │                               cuda-checkpoint --lock                 │
│       │                                      │                               │
│       │◄──────────────────────────────  SIGUSR1                             │
│       │                                      │                               │
│  ┌────▼────────────────────────┐             │                               │
│  │ Quiesce handler triggered   │             │                               │
│  │                             │             │                               │
│  │ 1. Set state = REQUESTED    │             │                               │
│  │ 2. For each io_uring:       │             │                               │
│  │    - io_uring_enter(GETEVS) │        sleep(2s)                            │
│  │    - Wait sq_head==sq_tail  │             │                               │
│  │    - Wait cq_head==cq_tail  │             │                               │
│  │ 3. Set state = COMPLETE     │             │                               │
│  │ 4. Spin wait for resume     │             │                               │
│  └─────────────────────────────┘             │                               │
│       │                                      │                               │
│       │ (blocked, io_uring empty)      CRIU dump                             │
│       │                                      │                               │
│       │                               cuda-checkpoint --unlock               │
│       │                                      │                               │
│       │◄──────────────────────────────  SIGUSR2 (if leave-running)          │
│       │                                      │                               │
│  ┌────▼────────────────────────┐             │                               │
│  │ Resume handler triggered    │             │                               │
│  │ Set state = NONE            │      Checkpoint complete                    │
│  │ Return from spin wait       │                                             │
│  └─────────────────────────────┘                                             │
│       │                                                                      │
│  Continues running...                                                        │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Restore Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            Restore Timeline                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  Restore Pod                                                                 │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  restore-entrypoint starts                                                   │
│       │                                                                      │
│       ▼                                                                      │
│  CRIU restore (process resurrected)                                          │
│       │                                                                      │
│       │  Process has stale state:                                            │
│       │  - io_uring FDs recreated by CRIU io_uring plugin                   │
│       │  - libuv epoll_fd invalid                                           │
│       │  - uvloop cached pointers stale                                      │
│       │                                                                      │
│  ┌────▼────────────────────────────────────────────────────────────────────┐│
│  │  libnvsnap_intercept.so __attribute__((constructor))                     ││
│  │                                                                          ││
│  │  Detects NVSNAP_RESTORED=1 environment variable                          ││
│  │       │                                                                  ││
│  │       ▼                                                                  ││
│  │  nvsnap_perform_restore_reinit()                                         ││
│  │  - Verify io_uring FDs still valid                                      ││
│  │  - Mark all libuv loops as needing reinit                               ││
│  │  - Set g_is_restored = 1                                                ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│       │                                                                      │
│  Application code resumes execution                                          │
│       │                                                                      │
│       ▼                                                                      │
│  First uv_run() call                                                         │
│       │                                                                      │
│  ┌────▼────────────────────────────────────────────────────────────────────┐│
│  │  Intercepted uv_run()                                                    ││
│  │                                                                          ││
│  │  nvsnap_ensure_libuv_loop_ready(loop)                                    ││
│  │  - Check if loop needs reinit                                           ││
│  │  - Call uv_loop_fork(loop)  ◄── Reinitializes epoll fd, signal state   ││
│  │  - Mark loop as reinitialized                                           ││
│  │                                                                          ││
│  │  Call real_uv_run(loop, mode)                                           ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│       │                                                                      │
│  Application continues with valid state                                      │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Implementation Details

### io_uring Interception

The library intercepts the `syscall()` libc function to catch io_uring syscalls:

```c
long syscall(long number, ...) {
    switch (number) {
        case __NR_io_uring_setup:
            // Track new io_uring instance
            ret = real_syscall(...);
            if (ret >= 0) {
                nvsnap_track_io_uring(ret, sq_entries, cq_entries, flags);
            }
            return ret;
            
        case __NR_close:
            // Untrack on close
            nvsnap_untrack_io_uring(fd);
            return real_syscall(...);
            
        default:
            // Check for quiesce on every syscall (opportunistic)
            nvsnap_perform_quiescence();
            return real_syscall(...);
    }
}
```

### io_uring Drain Algorithm

```c
static int drain_io_uring_instance(nvsnap_io_uring_instance_t* inst) {
    // Read current state from /proc/self/fdinfo/<fd>
    // Format: SqHead: N, SqTail: N, CqHead: N, CqTail: N
    
    while (sq_head != sq_tail || cq_head != cq_tail) {
        // Call io_uring_enter to process completions
        syscall(__NR_io_uring_enter, fd, 0, 0, IORING_ENTER_GETEVENTS, NULL, 0);
        
        // Re-check state
        // ...
        
        if (timeout_exceeded) break;
    }
}
```

### libuv Reinit Strategy

```c
int nvsnap_ensure_libuv_loop_ready(void* loop) {
    if (!g_is_restored) return 0;  // Fast path
    
    pthread_mutex_lock(&g_libuv_mutex);
    
    // Find loop in tracking table
    for (int i = 0; i < g_libuv_count; i++) {
        if (g_libuv_loops[i].loop == loop && !g_libuv_loops[i].reinitialized) {
            // Reinit via uv_loop_fork
            int err = real_uv_loop_fork(loop);
            g_libuv_loops[i].reinitialized = 1;
            break;
        }
    }
    
    pthread_mutex_unlock(&g_libuv_mutex);
    return 0;
}
```

## Kubernetes Integration

### Source Pod Configuration

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-gpu-app
  annotations:
    nvsnap.io/quiesce-enabled: "true"
spec:
  initContainers:
  # Copy intercept library from agent image
  - name: get-intercept-lib
    image: nvsnap-agent:latest
    command: ["cp", "/criu-bundle/lib/libnvsnap_intercept.so", "/nvsnap-lib/"]
    volumeMounts:
    - name: nvsnap-lib
      mountPath: /nvsnap-lib

  containers:
  - name: app
    image: my-gpu-app:latest
    env:
    # Enable io_uring/libuv tracking and quiescence
    - name: LD_PRELOAD
      value: "/nvsnap-lib/libnvsnap_intercept.so"
    - name: NVSNAP_LOG_LEVEL
      value: "3"  # info level
    volumeMounts:
    - name: nvsnap-lib
      mountPath: /nvsnap-lib
      readOnly: true

  volumes:
  - name: nvsnap-lib
    emptyDir: {}
```

### Restore Pod Configuration

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-gpu-app-restored
spec:
  containers:
  - name: restore
    image: my-gpu-app:latest
    command: ["/nvsnap/restore-entrypoint"]
    env:
    # Enable post-restore reinit
    - name: LD_PRELOAD
      value: "/nvsnap/lib/libnvsnap_intercept.so"
    - name: NVSNAP_RESTORED
      value: "1"  # Triggers libuv reinit on first loop use
    - name: NVSNAP_LOG_LEVEL
      value: "3"
```

## Handle-Level Reinitialization

### The Challenge

While `uv_loop_fork()` fixes libuv's loop-level backend state, individual handles also need attention:

```python
# uvloop's UVProcess class
class UVProcess:
    cdef uv_process_t* _handle  # Points to restored memory
```

After CRIU restore:
- The `uv_process_t` structure exists in memory (restored by CRIU)
- The handle's loop pointer is valid
- But the kernel-side state the handle references may need revalidation

### Our Solution: Lazy Handle Reinit

We track ALL libuv handles (not just loops) and reinit them on first use after restore:

```c
typedef struct nvsnap_uv_handle {
    void* handle;                   /* uv_handle_t* pointer */
    void* loop;                     /* Owning loop */
    nvsnap_uv_handle_type_t type;    /* PROCESS, SIGNAL, TCP, etc. */
    uint64_t generation;            /* Increments on restore */
    int needs_reinit;               /* Flag for lazy reinit */
} nvsnap_uv_handle_t;

/* Intercept all handle init functions */
int uv_tcp_init(void* loop, void* handle) {
    int ret = real_uv_tcp_init(loop, handle);
    if (ret == 0) {
        track_handle(handle, loop, NVSNAP_UV_TCP);
    }
    return ret;
}

/* Before any handle operation, ensure it's reinited */
int uv_timer_start(void* handle, void* cb, uint64_t timeout, uint64_t repeat) {
    ensure_handle_ready(handle);  /* Reinit if needed */
    return real_uv_timer_start(handle, cb, timeout, repeat);
}
```

### Process Handle Strategy

For `uv_process_t` handles (critical for vLLM's GPU workers):

1. **CRIU restores the entire process tree** - child processes are restored with original PIDs
2. **The handle's pid field remains valid** - points to the restored child
3. **Signal handling is reinited by `uv_loop_fork()`** - SIGCHLD delivery works
4. **We validate before any operation** - ensure handle state is consistent

```
Before Checkpoint:          After Restore:
┌─────────────┐             ┌─────────────┐
│ Parent      │             │ Parent      │
│ uv_process_t├──────┐      │ uv_process_t├──────┐
│ pid=1234    │      │      │ pid=1234    │      │  Same PID!
└─────────────┘      │      └─────────────┘      │
                     │                           │
                     ▼                           ▼
               ┌─────────┐                ┌─────────┐
               │ Child   │                │ Child   │  Also restored
               │ pid=1234│                │ pid=1234│  by CRIU
               └─────────┘                └─────────┘
```

### SQPOLL Handling

io_uring with `IORING_SETUP_SQPOLL` creates a kernel polling thread:

1. **Before checkpoint**: We drain the rings to ensure no pending I/O
2. **During restore**: CRIU's io_uring plugin recreates the ring structures
3. **After restore**: The SQPOLL thread is recreated by the kernel

Note: CPU affinity for the SQPOLL thread may need reapplication after restore

## Performance Considerations

### Overhead During Normal Operation

- **Syscall interception**: ~10-50ns per syscall (dlsym lookup cached)
- **Function interception**: ~5-20ns per libuv call
- **Memory**: ~50KB for tracking tables

### Overhead During Quiescence

- **io_uring drain**: Depends on pending I/O, typically <100ms
- **Default timeout**: 5 seconds max wait per ring

## Debugging

### Environment Variables

| Variable | Description |
|----------|-------------|
| `NVSNAP_LOG_LEVEL` | 0=off, 1=error, 2=warn, 3=info, 4=debug, 5=trace |
| `NVSNAP_RESTORED` | Set to "1" in restore pods to trigger reinit |
| `NVSNAP_ACK_FD` | (Future) FD for quiescence acknowledgment |

### Diagnostic Output

```
[09:16:25.746] [INFO] [nvsnap_init_explicit] === NVSNAP Interception Library Initializing ===
[09:16:25.746] [INFO] [nvsnap_init_explicit] PID: 117528
[09:16:25.755] [INFO] [nvsnap_quiesce_init] Quiesce module initialized (restored=0, ack_fd=-1)
[09:16:26.123] [INFO] [nvsnap_track_io_uring] Tracked io_uring fd=7 entries=256/512 flags=0x0 (SQPOLL=0)
[09:16:30.456] [INFO] [nvsnap_quiesce_io_uring] io_uring quiesce: 1/1 instances drained
```

## Future Enhancements

1. **Pipe-based ACK**: Replace sleep-based wait with explicit acknowledgment for faster checkpoints
2. **Selective quiescence**: Only quiesce rings that have pending I/O (optimization)
3. **Upstream contributions**: Contribute CRIU-compatibility improvements to uvloop/libuv
4. **Automatic detection**: Auto-detect if quiescence is needed based on /proc analysis
5. **Handle-specific reinit**: Per-handle-type optimized reinit (e.g., socket reconnection)
6. **Checkpoint validation**: Pre-flight checks to verify all tracked state is quiesced

## References

- [io_uring documentation](https://kernel.dk/io_uring.pdf)
- [libuv documentation](http://docs.libuv.org/)
- [CRIU documentation](https://criu.org/Main_Page)
- [uvloop source](https://github.com/MagicStack/uvloop)

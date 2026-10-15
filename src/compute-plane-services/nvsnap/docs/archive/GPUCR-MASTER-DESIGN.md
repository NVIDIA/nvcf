# NVSNAP: GPU Checkpoint/Restore for Kubernetes

## Master Architecture, Design & Implementation Document

**Version**: 1.0  
**Date**: January 15, 2026  
**Status**: Active Development

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Problem Statement](#problem-statement)
3. [Architecture Overview](#architecture-overview)
4. [Component Deep Dives](#component-deep-dives)
5. [Implementation Details](#implementation-details)
6. [Test Results & Issues Encountered](#test-results--issues-encountered)
7. [GPU Direct Storage for Fast Restore](#gpu-direct-storage-for-fast-restore)
8. [Inference Container Compatibility Matrix](#inference-container-compatibility-matrix)
9. [Appendix: Code References](#appendix-code-references)

---

## Executive Summary

NVSNAP enables transparent checkpoint and restore of GPU-accelerated containers in Kubernetes without requiring application modifications. This allows:

- **Fast cold starts**: Restore a warm, initialized model in seconds instead of minutes
- **Preemption recovery**: Save state before spot instance termination
- **Migration**: Move running GPU workloads between nodes
- **Debugging**: Capture state for offline analysis

### Key Achievements

| Capability | Status | Notes |
|------------|--------|-------|
| CRIU process checkpoint | ✅ Working | Custom fork with io_uring support |
| CUDA/GPU state checkpoint | ✅ Working | Via `cuda-checkpoint` binary |
| Network namespace handling | ✅ Working | InheritFd approach |
| Mount namespace handling | ✅ Working | ExtMnt explicit mappings |
| io_uring quiescence | ✅ Working | LD_PRELOAD interceptor |
| libuv/uvloop reinit | ✅ Working | Handle-level tracking |
| vLLM checkpoint/restore | 🔄 Testing | Worker process stability |

---

## Problem Statement

### Why GPU Checkpoint/Restore is Hard

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Traditional Process vs GPU Process                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  Traditional Process:              GPU Process:                              │
│  ┌─────────────────┐               ┌─────────────────┐                      │
│  │ CPU Memory      │               │ CPU Memory      │                      │
│  │ (checkpointable)│               │ (checkpointable)│                      │
│  └─────────────────┘               └─────────────────┘                      │
│                                            │                                 │
│                                            │ CUDA Context                    │
│                                            ▼                                 │
│                                    ┌─────────────────┐                      │
│                                    │ GPU Memory      │ ← NOT in /proc       │
│                                    │ (16-80GB VRAM)  │ ← Special handling   │
│                                    │ • Model weights │                      │
│                                    │ • KV cache      │                      │
│                                    │ • Activations   │                      │
│                                    └─────────────────┘                      │
│                                            │                                 │
│                                            │ Device State                    │
│                                            ▼                                 │
│                                    ┌─────────────────┐                      │
│                                    │ GPU Hardware    │                      │
│                                    │ • Streams       │                      │
│                                    │ • Events        │                      │
│                                    │ • Contexts      │                      │
│                                    └─────────────────┘                      │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Additional Challenges in Kubernetes

1. **Container Isolation**: Process runs in different namespaces (mount, network, PID)
2. **Ephemeral Networking**: Pod IPs change on restore
3. **Shared Storage**: Checkpoint data must be accessible across nodes
4. **Modern I/O**: Applications use io_uring, uvloop which have kernel state

---

## Architecture Overview

### System Components

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           NVSNAP System Architecture                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                         Kubernetes Cluster                               ││
│  │  ┌─────────────────────────────────────────────────────────────────┐    ││
│  │  │  Node (GPU)                                                      │    ││
│  │  │  ┌─────────────────┐   ┌─────────────────┐                      │    ││
│  │  │  │ NVSNAP Agent     │   │ Source Pod      │                      │    ││
│  │  │  │ (DaemonSet)     │   │                 │                      │    ││
│  │  │  │                 │   │ ┌─────────────┐ │                      │    ││
│  │  │  │ • HTTP API      │◄──┤►│ vLLM        │ │                      │    ││
│  │  │  │ • containerd    │   │ │ Container   │ │                      │    ││
│  │  │  │ • CRIU          │   │ │             │ │                      │    ││
│  │  │  │ • cuda-ckpt     │   │ │ LD_PRELOAD: │ │                      │    ││
│  │  │  │                 │   │ │ libnvsnap_   │ │                      │    ││
│  │  │  │                 │   │ │ intercept.so│ │                      │    ││
│  │  │  └────────┬────────┘   │ └─────────────┘ │                      │    ││
│  │  │           │            └─────────────────┘                      │    ││
│  │  │           │                                                      │    ││
│  │  │           ▼                                                      │    ││
│  │  │  ┌─────────────────┐                                            │    ││
│  │  │  │ Checkpoint      │                                            │    ││
│  │  │  │ Storage         │                                            │    ││
│  │  │  │ (hostPath/NFS)  │                                            │    ││
│  │  │  │                 │                                            │    ││
│  │  │  │ • CRIU images   │                                            │    ││
│  │  │  │ • GPU memory    │                                            │    ││
│  │  │  │ • metadata.json │                                            │    ││
│  │  │  └─────────────────┘                                            │    ││
│  │  └─────────────────────────────────────────────────────────────────┘    ││
│  │                                                                          ││
│  │  ┌─────────────────────────────────────────────────────────────────┐    ││
│  │  │  Node (GPU) - Restore Target                                     │    ││
│  │  │  ┌─────────────────┐   ┌─────────────────┐                      │    ││
│  │  │  │ NVSNAP Agent     │   │ Restore Pod     │                      │    ││
│  │  │  │                 │   │                 │                      │    ││
│  │  │  │                 │   │ ┌─────────────┐ │                      │    ││
│  │  │  │                 │   │ │ restore-    │ │                      │    ││
│  │  │  │                 │   │ │ entrypoint  │ │                      │    ││
│  │  │  │                 │   │ │             │ │                      │    ││
│  │  │  │                 │   │ │ • CRIU      │ │                      │    ││
│  │  │  │                 │   │ │ • cuda-ckpt │ │                      │    ││
│  │  │  └─────────────────┘   │ └─────────────┘ │                      │    ││
│  │  │                        └─────────────────┘                      │    ││
│  │  └─────────────────────────────────────────────────────────────────┘    ││
│  └─────────────────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────────────────┘
```

### Checkpoint Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Checkpoint Flow                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  Time ──────────────────────────────────────────────────────────────────►   │
│                                                                              │
│  Agent                    Container                    GPU                   │
│    │                         │                          │                    │
│    │  1. Find container      │                          │                    │
│    ├────────────────────────►│                          │                    │
│    │     (containerd API)    │                          │                    │
│    │                         │                          │                    │
│    │  2. cuda-checkpoint     │                          │                    │
│    │     --toggle-activate   │                          │                    │
│    ├─────────────────────────┼─────────────────────────►│                    │
│    │     (Lock GPU)          │                          │ GPU locked         │
│    │                         │                          │                    │
│    │  3. cuda-checkpoint     │                          │                    │
│    │     --checkpoint        │                          │                    │
│    ├─────────────────────────┼─────────────────────────►│                    │
│    │     (Save GPU memory)   │                          │ → checkpoint/      │
│    │                         │                          │                    │
│    │  4. SIGUSR1 (quiesce)   │                          │                    │
│    ├────────────────────────►│                          │                    │
│    │                         │ Drain io_uring           │                    │
│    │                         │ Prepare libuv            │                    │
│    │                         │                          │                    │
│    │  5. CRIU dump           │                          │                    │
│    ├────────────────────────►│                          │                    │
│    │     (Process state)     │ Process frozen           │                    │
│    │                         │ → checkpoint/            │                    │
│    │                         │                          │                    │
│    │  6. cuda-checkpoint     │                          │                    │
│    │     --toggle-activate   │                          │                    │
│    ├─────────────────────────┼─────────────────────────►│                    │
│    │     (Unlock GPU)        │                          │ GPU unlocked       │
│    │                         │                          │                    │
│    │  7. Save metadata.json  │                          │                    │
│    ├────────────────────────►│                          │                    │
│    │                         │                          │                    │
│    ▼                         ▼                          ▼                    │
│                                                                              │
│  Checkpoint Directory:                                                       │
│  /var/lib/kubelet/nvsnap-checkpoints/podname__namespace__timestamp/          │
│  ├── core-*.img          (CRIU process images)                              │
│  ├── pages-*.img         (Process memory)                                   │
│  ├── cuda-checkpoint/    (GPU memory dumps)                                 │
│  ├── mapped-files/       (JIT-compiled code)                                │
│  └── metadata.json       (Checkpoint metadata)                              │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Restore Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            Restore Flow                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  restore-entrypoint              CRIU                  cuda-checkpoint       │
│    │                              │                          │               │
│    │  1. Read metadata.json       │                          │               │
│    ├─────────────────────────────►│                          │               │
│    │                              │                          │               │
│    │  2. Setup loopback alias     │                          │               │
│    │     (for original pod IP)    │                          │               │
│    ├──────────────────────────────│                          │               │
│    │                              │                          │               │
│    │  3. Setup egress guardrails  │                          │               │
│    │     (prevent IP leakage)     │                          │               │
│    ├──────────────────────────────│                          │               │
│    │                              │                          │               │
│    │  4. Ensure ExtMnt targets    │                          │               │
│    │     (create missing dirs)    │                          │               │
│    ├──────────────────────────────│                          │               │
│    │                              │                          │               │
│    │  5. Build CRIU options       │                          │               │
│    │     (ExtMnt, InheritFd...)   │                          │               │
│    ├──────────────────────────────│                          │               │
│    │                              │                          │               │
│    │  6. go-criu Restore()        │                          │               │
│    ├─────────────────────────────►│                          │               │
│    │                              │ Restore process          │               │
│    │                              │ Re-create namespaces     │               │
│    │                              │ Restore memory           │               │
│    │                              │ Restore FDs              │               │
│    │                              │                          │               │
│    │                              │  7. CUDA plugin hook     │               │
│    │                              ├─────────────────────────►│               │
│    │                              │                          │ Restore GPU   │
│    │                              │                          │ memory        │
│    │                              │                          │               │
│    │  8. Process resumes          │                          │               │
│    │◄────────────────────────────┤│                          │               │
│    │                              │                          │               │
│    │  9. libnvsnap_intercept.so    │                          │               │
│    │     detects NVSNAP_RESTORED=1 │                          │               │
│    │     → Reinit libuv handles   │                          │               │
│    │                              │                          │               │
│    │  10. Monitor process         │                          │               │
│    │      (reap children, log)    │                          │               │
│    ▼                              ▼                          ▼               │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Component Deep Dives

### 1. CRIU (Checkpoint/Restore in Userspace)

We maintain a custom CRIU fork with enhancements for GPU workloads:

**Repository**: `github.com/balaji-g/criu` (tag: `nvsnap-v0.2.0`)

**Key Modifications**:

| Feature | File | Description |
|---------|------|-------------|
| io_uring support | `criu/io_uring.c` | Quiescence gate, inode grouping, SQE128/CQE32 |
| CUDA plugin | `plugins/cuda/` | Integrates with `cuda-checkpoint` binary |
| Network lock skip | `criu/net.c` | Skip iptables (not available in all containers) |

**io_uring Improvements**:

```c
// Before our fix: CRIU would checkpoint with pending I/O
// After: Fail dump if rings not empty

static int dump_io_uring(int fd) {
    // Quiescence gate
    if (fdinfo.sq_head != fdinfo.sq_tail) {
        pr_err("io_uring fd %d has pending submissions\n", fd);
        return -1;
    }
    if (fdinfo.cq_head != fdinfo.cq_tail) {
        pr_err("io_uring fd %d has pending completions\n", fd);
        return -1;
    }
    // ... proceed with dump
}
```

### 2. cuda-checkpoint

NVIDIA's binary for GPU context checkpoint/restore.

**Operations**:

| Command | Description |
|---------|-------------|
| `--toggle-activate` | Lock/unlock GPU (freeze CUDA operations) |
| `--checkpoint` | Save GPU memory to files |
| `--restore` | Restore GPU memory from files |

**Integration Points**:

```go
// internal/cuda/cuda.go
func (c *CUDAManager) Lock(ctx context.Context, pid int) error {
    cmd := exec.Command(c.cudaCheckpointPath, "--toggle-activate", "-p", strconv.Itoa(pid))
    return cmd.Run()
}

func (c *CUDAManager) Checkpoint(ctx context.Context, pid int) error {
    cmd := exec.Command(c.cudaCheckpointPath, "--checkpoint", "-p", strconv.Itoa(pid), 
                        "-d", c.checkpointDir)
    return cmd.Run()
}
```

### 3. libnvsnap_intercept.so

LD_PRELOAD library for generic quiescence and reinit:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        libnvsnap_intercept.so                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────────┐  │
│  │  io_uring_intercept │  │  libuv_intercept    │  │  quiesce            │  │
│  │                     │  │                     │  │                     │  │
│  │  Intercepts:        │  │  Intercepts:        │  │  Coordinates:       │  │
│  │  • syscall()        │  │  • uv_loop_init     │  │  • SIGUSR1 handler  │  │
│  │  • io_uring_setup   │  │  • uv_run           │  │  • SIGUSR2 handler  │  │
│  │  • io_uring_enter   │  │  • uv_*_init        │  │  • Drain io_uring   │  │
│  │  • liburing funcs   │  │  • uv_spawn         │  │  • Reinit libuv     │  │
│  │                     │  │  • uv_close         │  │                     │  │
│  │  Tracks:            │  │  • uv_timer_*       │  │  States:            │  │
│  │  • Ring FDs         │  │  • uv_signal_*      │  │  • NONE             │  │
│  │  • SQ/CQ sizes      │  │  • uv_async_*       │  │  • REQUESTED        │  │
│  │  • SQPOLL flags     │  │                     │  │  • COMPLETE         │  │
│  │                     │  │  Tracks:            │  │  • RESUMED          │  │
│  │                     │  │  • Loop pointers    │  │                     │  │
│  │                     │  │  • Handle pointers  │  │                     │  │
│  │                     │  │  • Handle types     │  │                     │  │
│  │                     │  │  • Generation       │  │                     │  │
│  └─────────────────────┘  └─────────────────────┘  └─────────────────────┘  │
│                                                                              │
│  On SIGUSR1 (pre-checkpoint):                                               │
│  1. Drain all io_uring rings (wait for SQ/CQ empty)                        │
│  2. Mark libuv loops/handles for reinit                                     │
│  3. Spin wait until SIGUSR2 or checkpoint                                   │
│                                                                              │
│  On restore (NVSNAP_RESTORED=1):                                             │
│  1. Increment generation counter                                            │
│  2. Mark all handles as needs_reinit                                        │
│  3. On first handle use: call uv_loop_fork(), validate handle               │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4. restore-entrypoint

Go binary that runs inside the restore pod:

**Key Functions**:

| Function | Purpose |
|----------|---------|
| `setupLoopbackAlias()` | Add original pod IP to lo interface |
| `setupEgressGuardrails()` | Prevent source IP leakage via routing rules |
| `ensureExtMountTargets()` | Create missing directories for bind mounts |
| `buildExtMntMappings()` | Generate CRIU ExtMnt options from mountinfo |
| `monitorProcess()` | Reap children, capture exit status |

**Network Setup**:

```go
func setupLoopbackAlias(originalPodIP string) error {
    // Add original IP to loopback so restored process can bind to it
    lo, _ := netlink.LinkByName("lo")
    addr := &netlink.Addr{
        IPNet: &net.IPNet{IP: net.ParseIP(originalPodIP), Mask: ...},
    }
    netlink.AddrAdd(lo, addr)
    
    // Add routing rule to prevent source IP leakage
    rule := netlink.NewRule()
    rule.Src = addr.IPNet
    rule.Table = 100  // Local table
    netlink.RuleAdd(rule)
}
```

---

## Implementation Details

### Kubernetes Pod Manifests

**Source Pod** (`deploy/k8s/vllm-small.yaml`):

```yaml
spec:
  initContainers:
  # Copy intercept library
  - name: get-intercept-lib
    image: nvsnap-agent:v0.4.6
    command: ["cp", "/criu-bundle/lib/libnvsnap_intercept.so", "/nvsnap-lib/"]
    
  containers:
  - name: vllm
    image: vllm/vllm-openai:v0.6.6.post1
    env:
    # Enable io_uring/libuv tracking
    - name: LD_PRELOAD
      value: "/nvsnap-lib/libnvsnap_intercept.so"
    # Python uvloop disable (fallback)
    - name: PYTHONPATH
      value: "/python-patches"
```

**Restore Pod** (`deploy/k8s/vllm-restore-pod.yaml`):

```yaml
spec:
  initContainers:
  - name: get-criu
    image: nvsnap-agent:v0.4.6
    command: ["cp", "-r", "/criu-bundle/.", "/nvsnap/"]
    
  containers:
  - name: restore
    image: vllm/vllm-openai:v0.6.6.post1
    command: ["/nvsnap/restore-entrypoint"]
    env:
    - name: CHECKPOINT_ID
      value: "vllm-small__nvsnap-system__20260115-000000"
    - name: LD_PRELOAD
      value: "/nvsnap/lib/libnvsnap_intercept.so"
    - name: NVSNAP_RESTORED
      value: "1"  # Triggers handle reinit
```

### Checkpoint Metadata Schema

```json
{
  "version": "1.3",
  "id": "vllm-small__nvsnap-system__20260115-162351",
  "createdAt": "2026-01-15T16:23:51Z",
  "podName": "vllm-small",
  "podNamespace": "nvsnap-system",
  "containerPid": 1234567,
  "rootfs": "/run/containerd/io.containerd.runtime.v2.task/k8s.io/abc123/rootfs",
  
  "source_pod_ip": "10.244.1.5",
  "stdout_pipe_id": "pipe:[12345]",
  "stderr_pipe_id": "pipe:[12346]",
  
  "dumpMountPoints": [
    "/",
    "/dev/shm",
    "/run/nvidia-container-devices/GPU-xxx"
  ],
  
  "cuda": {
    "enabled": true,
    "gpuPid": 1234567,
    "lockSuccess": true,
    "checkpointSuccess": true
  },
  
  "restoreHints": {
    "cudaRestoreNeeded": true,
    "networkMode": "external"
  }
}
```

---

## Test Results & Issues Encountered

### Chronological Test Log

#### Issue 1: Network Namespace Restore Failure

**Symptom**:
```
Error (criu/net.c:1469): net: Unknown peer net namespace err:0
```

**Root Cause**: Using `JoinNs` for network namespace doesn't work with container network namespaces.

**Solution**: Switch to `InheritFd` approach - open `/proc/<pid>/ns/net` and pass FD to CRIU.

```go
// Before (broken)
criuOpts.JoinNs = []*criurpc.JoinNamespace{{
    Ns: proto.String("net"),
    NsFile: proto.String(netnsPath),
}}

// After (working)
netNsFile, _ := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
criuOpts.InheritFd = []*criurpc.InheritFd{{
    Key: proto.String("extNetNs"),
    Fd: proto.Int32(int32(netNsFile.Fd())),
}}
criuOpts.External = []string{fmt.Sprintf("net[%d]:extNetNs", netnsInode)}
```

#### Issue 2: Missing Mount Targets

**Symptom**:
```
Error (criu/mount.c:2540): mnt: Can't bind-mount at 
/tmp/.criu.mntns.xxx/run/nvidia-container-devices/GPU-xxx: No such file or directory
```

**Root Cause**: CRIU tries to bind-mount to paths that don't exist in restore container.

**Solution**: Pre-create all mount targets before CRIU restore.

```go
func ensureExtMountTargets(extMnts []*criurpc.ExtMountMap) error {
    for _, mnt := range extMnts {
        path := mnt.GetVal()
        if strings.HasPrefix(path, "/proc") || strings.HasPrefix(path, "/sys") {
            continue  // Skip pseudo-filesystems
        }
        
        // Determine if file or directory based on existing content or name
        if shouldBeFile(path) {
            os.MkdirAll(filepath.Dir(path), 0755)
            os.Create(path)
        } else {
            os.MkdirAll(path, 0755)
        }
    }
}
```

#### Issue 3: iptables-restore Not Found

**Symptom**:
```
Error (criu/util.c:641): execvp("iptables-restore", ...) failed: No such file or directory
```

**Root Cause**: CRIU's network lock mechanism tries to call `iptables-restore` which doesn't exist in minimal container images.

**Solution**: Bundle no-op shims in CRIU bundle.

```dockerfile
# In Dockerfile
RUN printf '#include <stdlib.h>\nint main(){return 0;}\n' > /tmp/true.c && \
    gcc -static -O2 -s -o /criu-bundle/iptables-restore /tmp/true.c && \
    cp /criu-bundle/iptables-restore /criu-bundle/ip6tables-restore
```

#### Issue 4: io_uring SQPOLL Segfault

**Symptom**:
```
CRIU segfaults at "Obtaining task auxv..." when checkpointing uvloop with io_uring
```

**Root Cause**: `IORING_SETUP_SQPOLL` creates a kernel polling thread (`iou-sqp-<pid>`) that actively accesses the ring memory. CRIU's parasite injection races with this thread.

**Solution**: Drain io_uring rings before checkpoint via libnvsnap_intercept.so.

```c
// On SIGUSR1 (pre-checkpoint)
static void nvsnap_quiesce_io_uring(void) {
    for (each tracked io_uring) {
        // Wait for rings to be empty
        while (sq_head != sq_tail || cq_head != cq_tail) {
            io_uring_enter(fd, 0, 0, IORING_ENTER_GETEVENTS);
            // Re-check state
        }
    }
}
```

#### Issue 5: uvloop Worker Process Crash

**Symptom**:
```
RuntimeError('Engine process (pid 76) died.')
vLLM GPU worker dies ~60s after restore
```

**Root Cause**: uvloop's Python objects contain cached C pointers to libuv handles. After restore, these handles need reinitialization.

**Solution**: Track ALL libuv handles and reinit on first use after restore.

```c
// Track every handle
int uv_process_init(void* loop, void* handle) {
    int ret = real_uv_process_init(loop, handle);
    if (ret == 0) {
        track_handle(handle, loop, NVSNAP_UV_PROCESS);
    }
    return ret;
}

// Reinit before use
int uv_timer_start(void* handle, ...) {
    ensure_handle_ready(handle);  // Reinit if needed
    return real_uv_timer_start(handle, ...);
}
```

#### Issue 6: Checkpoint Cleanup Data Loss

**Symptom**: All previous checkpoints deleted when new checkpoint fails.

**Root Cause**: `cleanupOldPodCheckpoints()` was called at the start of checkpoint operation, before success.

**Solution**: Move cleanup to after successful checkpoint creation.

```go
// Before (buggy)
func (a *Agent) Checkpoint(...) {
    a.cleanupOldPodCheckpoints(...)  // ← Deletes all before trying!
    // ... checkpoint logic that might fail
}

// After (fixed)
func (a *Agent) Checkpoint(...) {
    // ... checkpoint logic
    if err != nil {
        return err  // Don't clean up on failure
    }
    a.cleanupOldPodCheckpoints(...)  // ← Only after success
    return result, nil
}
```

### Test Matrix

| Test Case | Status | Notes |
|-----------|--------|-------|
| Simple CUDA allocation | ✅ Pass | Basic GPU memory checkpoint |
| Multi-stream CUDA | ✅ Pass | Multiple CUDA streams |
| vLLM TinyLlama startup | ✅ Pass | Model loads, serves requests |
| vLLM checkpoint | ✅ Pass | ~5s for 1.1B model |
| vLLM restore | 🔄 Testing | Worker stability under investigation |
| Network namespace | ✅ Pass | InheritFd approach works |
| Mount namespace | ✅ Pass | ExtMnt mappings work |
| io_uring (epoll backend) | ✅ Pass | UV_USE_IO_URING=0 |
| io_uring (native) | ✅ Pass | With quiescence |
| uvloop handles | 🔄 Testing | Handle-level reinit |

---

## GPU Direct Storage for Fast Restore

### Current Restore Bottleneck

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Current Restore Path (Slow)                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   Storage                   CPU Memory                  GPU Memory           │
│  ┌─────────┐               ┌─────────┐                ┌─────────┐           │
│  │ NFS/    │   PCIe/Net    │         │    PCIe       │         │           │
│  │ Local   │ ──────────►   │  RAM    │ ──────────►   │  VRAM   │           │
│  │ Disk    │    ~2GB/s     │         │    ~32GB/s    │         │           │
│  │         │               │         │                │         │           │
│  │ 16GB    │               │ 16GB    │                │ 16GB    │           │
│  │ model   │               │ staging │                │ final   │           │
│  └─────────┘               └─────────┘                └─────────┘           │
│                                                                              │
│  Total time for 16GB model: ~8s (NFS) to ~16s (slow disk)                   │
│  Bottleneck: Storage → CPU → GPU double copy                                │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### GPU Direct Storage (GDS) Solution

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    GDS Restore Path (Fast)                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   NVMe Storage                                          GPU Memory           │
│  ┌─────────────┐            Direct Path                ┌─────────┐          │
│  │             │                                       │         │          │
│  │  Local NVMe │ ─────────────────────────────────────►│  VRAM   │          │
│  │  (GDS-aware)│        ~6.5GB/s (4x NVMe)            │         │          │
│  │             │                                       │         │          │
│  │  16GB model │           PCIe Gen4 x16              │ 16GB    │          │
│  │             │           No CPU bounce              │ final   │          │
│  └─────────────┘                                       └─────────┘          │
│                                                                              │
│  Total time for 16GB model: ~2.5s                                           │
│  Speedup: 3-6x over traditional path                                        │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Implementation Approach

#### Option A: cuFile API Integration

```c
// In cuda-checkpoint restore path
#include <cufile.h>

int restore_gpu_memory_gds(const char* checkpoint_dir, CUdeviceptr gpu_ptr, size_t size) {
    // Open checkpoint file with GDS
    CUfileHandle_t fh;
    CUfileDescr_t desc;
    desc.type = CU_FILE_HANDLE_TYPE_OPAQUE_FD;
    desc.handle.fd = open(checkpoint_file, O_RDONLY | O_DIRECT);
    cuFileHandleRegister(&fh, &desc);
    
    // Register GPU buffer
    cuFileBufRegister(gpu_ptr, size, 0);
    
    // Direct read: Storage → GPU (bypasses CPU)
    cuFileRead(fh, gpu_ptr, size, 0, 0);
    
    // Cleanup
    cuFileBufDeregister(gpu_ptr);
    cuFileHandleDeregister(fh);
}
```

#### Option B: NVIDIA Magnum IO Integration

For larger deployments, integrate with NVIDIA's Magnum IO stack:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Magnum IO Stack                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                         Application (NVSNAP)                              ││
│  └───────────────────────────────┬─────────────────────────────────────────┘│
│                                  │                                           │
│  ┌─────────────────┬─────────────┴─────────────┬─────────────────┐          │
│  │                 │                           │                 │          │
│  │  GPUDirect      │  GPUDirect               │  GPUDirect      │          │
│  │  Storage (GDS)  │  RDMA                    │  Video          │          │
│  │                 │                           │                 │          │
│  │  NVMe → GPU     │  NIC → GPU               │  Camera → GPU   │          │
│  │                 │  (for distributed ckpt)   │                 │          │
│  └─────────────────┴───────────────────────────┴─────────────────┘          │
│                                  │                                           │
│  ┌───────────────────────────────┴─────────────────────────────────────────┐│
│  │                         CUDA Driver                                      ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### Option C: Pre-staged Checkpoint Cache

For frequently-used models, keep checkpoint in local NVMe:

```yaml
# Node-local checkpoint cache
apiVersion: v1
kind: DaemonSet
metadata:
  name: nvsnap-checkpoint-cache
spec:
  template:
    spec:
      containers:
      - name: cache-manager
        volumeMounts:
        - name: nvme-cache
          mountPath: /cache
        - name: checkpoint-nfs
          mountPath: /checkpoints
        env:
        - name: POPULAR_MODELS
          value: "llama-7b,mistral-7b,tinyllama"
      volumes:
      - name: nvme-cache
        hostPath:
          path: /mnt/nvme/nvsnap-cache
```

### Expected Performance Gains

| Model Size | Current (NFS) | Current (Local) | With GDS | Speedup |
|------------|---------------|-----------------|----------|---------|
| 1GB | ~1s | ~0.5s | ~0.15s | 3-6x |
| 7GB | ~7s | ~3.5s | ~1.1s | 3-6x |
| 13GB | ~13s | ~6.5s | ~2s | 3-6x |
| 70GB | ~70s | ~35s | ~11s | 3-6x |

### Requirements for GDS

1. **Hardware**: 
   - NVIDIA Ampere or newer GPU
   - NVMe drives with GDS support
   - PCIe Gen4 x16 for full bandwidth

2. **Software**:
   - CUDA 12.x+
   - GDS driver (`nvidia-fs` kernel module)
   - cuFile library

3. **Filesystem**:
   - ext4 or XFS with GDS support
   - Or: raw block device access

---

## Inference Container Compatibility Matrix

### Priority Testing Order

| Priority | Container | Framework | Notes |
|----------|-----------|-----------|-------|
| 1 | vLLM | PyTorch | Current focus, largest user base |
| 2 | SGLang | PyTorch | Growing adoption, similar architecture |
| 3 | TensorRT-LLM | TensorRT | NVIDIA's optimized inference |
| 4 | Text Generation Inference (TGI) | PyTorch/Rust | HuggingFace's solution |
| 5 | llama.cpp | C++ | CPU-focused but has CUDA backend |
| 6 | Triton Inference Server | Multiple | NVIDIA's serving platform |

### Known Compatibility Considerations

#### vLLM
```
Status: 🔄 In Progress

Challenges:
- uvloop for async I/O → Handle-level reinit
- multiprocessing for GPU workers → Process tree restore
- Ray (optional) for distributed → More complex process tree

Mitigations:
- libnvsnap_intercept.so handles uvloop
- CRIU restores entire process tree
- Ray support TBD
```

#### SGLang
```
Status: 📋 Planned

Expected Challenges:
- Similar to vLLM (same PyTorch/asyncio base)
- RadixAttention for prefix caching → May need special handling
- Frontend/backend split → Two process checkpoint

Testing Plan:
1. Basic checkpoint/restore with simple model
2. Test with prefix caching enabled
3. Test distributed setup
```

#### TensorRT-LLM
```
Status: 📋 Planned

Expected Challenges:
- Different CUDA usage patterns (more optimized)
- Engine compilation state → May be large
- Quantized models → Verify CUDA checkpoint handles

Testing Plan:
1. Basic checkpoint with INT8 model
2. Test with in-flight batching
3. Test with paged KV cache
```

#### Text Generation Inference (TGI)
```
Status: 📋 Planned

Expected Challenges:
- Rust async runtime (tokio) → Different from uvloop
- Flash attention → Specialized CUDA kernels
- Continuous batching → State complexity

Testing Plan:
1. Basic checkpoint
2. Test with flash attention
3. Test with speculation
```

### Testing Checklist Template

For each inference container:

```markdown
## [Container Name] Compatibility Testing

### Environment Setup
- [ ] Container image and version
- [ ] Model used for testing
- [ ] GPU type and memory

### Basic Functionality
- [ ] Container starts normally
- [ ] Model loads successfully
- [ ] Inference requests work

### Checkpoint Testing
- [ ] Agent finds container
- [ ] GPU lock succeeds
- [ ] GPU checkpoint succeeds
- [ ] Process quiesce succeeds
- [ ] CRIU dump succeeds
- [ ] Checkpoint size reasonable

### Restore Testing
- [ ] Restore pod deploys
- [ ] CRIU restore succeeds
- [ ] GPU restore succeeds
- [ ] Process resumes
- [ ] Inference requests work post-restore
- [ ] No memory leaks observed

### Stress Testing
- [ ] Multiple checkpoint/restore cycles
- [ ] Checkpoint under load
- [ ] Restore to different node

### Issues Found
- Issue 1: ...
- Issue 2: ...
```

---

## Appendix: Code References

### Key Files

| File | Purpose |
|------|---------|
| `cmd/agent/main.go` | Agent entry point |
| `internal/agent/checkpoint.go` | Checkpoint orchestration |
| `internal/agent/checkpoint_plan_a.go` | ExtMnt/RPC dump logic |
| `cmd/restore-entrypoint/main.go` | Restore orchestration |
| `lib/nvsnap_intercept/src/quiesce.c` | Quiescence coordinator |
| `lib/nvsnap_intercept/src/io_uring_intercept.c` | io_uring tracking |
| `lib/nvsnap_intercept/src/libuv_intercept.c` | libuv/handle tracking |
| `docker/agent/Dockerfile` | Agent image build |
| `deploy/k8s/vllm-small.yaml` | Source pod manifest |
| `deploy/k8s/vllm-restore-pod.yaml` | Restore pod manifest |

### Build Commands

```bash
# Build agent image
./scripts/build-agent-image.sh

# Build intercept library locally
cd lib/nvsnap_intercept && make

# Build Go binaries
go build ./cmd/agent
go build ./cmd/restore-entrypoint
```

### Deployment Commands

```bash
# Set kubeconfig
export KUBECONFIG=/path/to/kubeconfig

# Deploy agent DaemonSet
kubectl apply -f deploy/agent-daemonset.yaml

# Deploy source pod
kubectl apply -f deploy/k8s/vllm-small.yaml

# Trigger checkpoint
kubectl exec -n nvsnap-system nvsnap-agent-xxx -- \
  curl -X POST http://localhost:8081/checkpoint \
  -d '{"namespace":"nvsnap-system","podName":"vllm-small"}'

# Deploy restore pod
kubectl apply -f deploy/k8s/vllm-restore-pod.yaml
```

---

## Document History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-01-15 | Initial comprehensive document |

---

*This document is maintained as part of the NVSNAP project. For questions or contributions, see the repository README.*

# NVSNAP Production Architecture

## The Fundamental Question

What are we actually trying to achieve?

**Goal**: Save a running GPU container's complete state and restore it later.

**State Components**:
1. **Process State** (CPU registers, memory, file descriptors) → CRIU
2. **Container State** (namespaces, cgroups, rootfs) → containerd
3. **GPU State** (CUDA context, GPU memory) → cuda-checkpoint

---

## Option A: Direct Library Integration

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           nvsnap-agent (Go)                                   │
│                                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────────┐  │
│  │  containerd client  │  │     go-criu         │  │  cuda-checkpoint    │  │
│  │  (Go library)       │  │   (Go library)      │  │  (exec - no choice) │  │
│  │                     │  │                     │  │                     │  │
│  │  - Container CRUD   │  │  - Process dump     │  │  - GPU lock         │  │
│  │  - Task management  │  │  - Process restore  │  │  - GPU checkpoint   │  │
│  │  - Namespace access │  │  - Page server      │  │  - GPU restore      │  │
│  └──────────┬──────────┘  └──────────┬──────────┘  └──────────┬──────────┘  │
│             │                        │                        │             │
└─────────────┼────────────────────────┼────────────────────────┼─────────────┘
              │                        │                        │
              ▼                        ▼                        ▼
    /run/containerd/            CRIU syscalls              cuda-checkpoint
    containerd.sock                                           binary
```

### Go Dependencies

```go
import (
    // containerd client
    "github.com/containerd/containerd"
    "github.com/containerd/containerd/cio"
    "github.com/containerd/containerd/namespaces"
    
    // CRIU client
    "github.com/checkpoint-restore/go-criu/v7"
    "github.com/checkpoint-restore/go-criu/v7/rpc"
    
    // For cuda-checkpoint (no Go library exists)
    "os/exec"
)
```

### Checkpoint Flow (Library-based)

```go
func (a *Agent) Checkpoint(ctx context.Context, containerID string) error {
    // 1. Connect to containerd
    client, err := containerd.New("/run/containerd/containerd.sock",
        containerd.WithDefaultNamespace("k8s.io"))
    
    // 2. Get container and task
    container, err := client.LoadContainer(ctx, containerID)
    task, err := container.Task(ctx, nil)
    pids, err := task.Pids(ctx)
    mainPID := pids[0].Pid
    
    // 3. Lock GPU (must use exec - no Go library)
    gpuPID := a.findGPUProcess(mainPID)
    exec.Command("cuda-checkpoint", "--action", "lock", "--pid", gpuPID).Run()
    exec.Command("cuda-checkpoint", "--action", "checkpoint", "--pid", gpuPID).Run()
    
    // 4. Checkpoint process with go-criu
    c := criu.MakeCriu()
    opts := &rpc.CriuOpts{
        Pid:            proto.Int32(int32(mainPID)),
        ImagesDir:      proto.String(checkpointDir),
        LogLevel:       proto.Int32(4),
        ShellJob:       proto.Bool(true),
        LeaveRunning:   proto.Bool(false),
        TcpEstablished: proto.Bool(true),
        // Plugin for GPU device handling
        External:       []string{"dev[nvidia]:/dev/nvidia*"},
    }
    err = c.Dump(opts, notifyCallback)
    
    // 5. Save container metadata for restore
    spec, _ := container.Spec(ctx)
    saveMetadata(containerID, spec, checkpointDir)
    
    return nil
}
```

### Restore Flow (Library-based)

```go
func (a *Agent) Restore(ctx context.Context, checkpointDir string) error {
    // 1. Load metadata
    metadata := loadMetadata(checkpointDir)
    
    // 2. Create new container with same spec
    client, _ := containerd.New("/run/containerd/containerd.sock")
    container, _ := client.NewContainer(ctx, newContainerID,
        containerd.WithSpec(metadata.Spec),
        containerd.WithSnapshotter("overlayfs"),
        containerd.WithImage(image),
    )
    
    // 3. Create task but DON'T start it
    task, _ := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
    
    // 4. Get the task's PID namespace
    taskPID, _ := task.Pid()
    
    // 5. Restore process with go-criu into the container's namespaces
    c := criu.MakeCriu()
    opts := &rpc.CriuOpts{
        ImagesDir:      proto.String(checkpointDir),
        Root:           proto.String(containerRootfs),
        ShellJob:       proto.Bool(true),
        RestoreDetach:  proto.Bool(true),
        // Inherit the container's namespaces
        InheritFd:      []*rpc.InheritFd{
            {Fd: proto.Int32(netNsFd), Key: proto.String("net")},
        },
    }
    err = c.Restore(opts, notifyCallback)
    
    // 6. Restore GPU state
    newPID := getRestoredPID()
    exec.Command("cuda-checkpoint", "--action", "restore", "--pid", newPID).Run()
    exec.Command("cuda-checkpoint", "--action", "unlock", "--pid", newPID).Run()
    
    return nil
}
```

### Pros/Cons of Option A

**Pros:**
- Clean Go code, proper libraries
- Type-safe, testable
- Production quality

**Cons:**
- Complex - still fighting namespace injection
- go-criu restore into existing container is tricky
- Still need to coordinate containerd + CRIU + cuda-checkpoint

---

## Option B: Simpler Approach - What Are We Missing?

### Key Insight

The reason checkpoint/restore is hard is because we're trying to:
1. Checkpoint a CONTAINER (complex: namespaces, rootfs, cgroups)
2. Restore into a DIFFERENT container

### Alternative: Don't checkpoint the container, checkpoint the WORKLOAD

For ML inference workloads like vLLM, the "state" is really:
- Model weights → Already on disk/PVC
- KV cache → In GPU memory (cuda-checkpoint handles this)
- Request queue → In process memory

What if we:
1. **Don't** use container-level checkpoint
2. **Only** checkpoint the process + GPU state
3. Restore into a fresh container with the same image

### Simpler Flow

```
CHECKPOINT:
1. Pod running vLLM
2. cuda-checkpoint --lock (freeze GPU)
3. cuda-checkpoint --checkpoint (save GPU memory)
4. CRIU dump the vLLM process (save CPU/memory state)
5. Save checkpoint to storage
6. (Optional) Kill pod

RESTORE:
1. Create new pod with same spec
2. Wait for container to start
3. IMMEDIATELY kill the entrypoint process
4. CRIU restore into the container's rootfs/namespaces
5. cuda-checkpoint --restore
6. cuda-checkpoint --unlock
7. vLLM continues from where it left off
```

### Why This Is Simpler

- We don't need containerd's checkpoint API
- We just need:
  - CRIU (with proper NVIDIA plugin)
  - cuda-checkpoint
  - A way to inject into a container's namespace

### The Critical Piece: Namespace Injection

The key is: can we restore a process INTO an existing container?

Answer: **YES**, using CRIU's `--inherit-fd` and namespace joining:

```bash
# Get container's namespace FDs
PID=$(crictl inspect $CONTAINER_ID | jq '.info.pid')
NETNS=/proc/$PID/ns/net
MNTNS=/proc/$PID/ns/mnt
PIDNS=/proc/$PID/ns/pid

# CRIU restore into those namespaces
criu restore \
    --images-dir /checkpoint \
    --root /path/to/container/rootfs \
    --inherit-fd "fd[0]:$NETNS" \
    --join-ns "net:$NETNS" \
    --join-ns "mnt:$MNTNS" \
    --pidfile /tmp/restored.pid
```

---

## Recommendation: Hybrid Approach

Combine the best of both:

1. **Use containerd Go client** for:
   - Finding containers
   - Getting container metadata
   - Creating new containers for restore

2. **Use go-criu directly** for:
   - Process checkpoint (not container checkpoint)
   - Process restore with namespace injection

3. **Use cuda-checkpoint exec** for:
   - GPU state (no alternative)

### Final Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              nvsnap-agent                                     │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                        Checkpoint Manager                               │ │
│  │                                                                         │ │
│  │  checkpoint(pod) {                                                      │ │
│  │    container = containerdClient.getContainer(pod)                       │ │
│  │    pid = container.task.pid                                            │ │
│  │    gpuPid = findGPUProcess(pid)                                        │ │
│  │                                                                         │ │
│  │    cudaCheckpoint.lock(gpuPid)                                         │ │
│  │    cudaCheckpoint.checkpoint(gpuPid)                                   │ │
│  │    goCriu.dump(pid, opts)                                              │ │
│  │    saveMetadata(container.spec)                                        │ │
│  │  }                                                                      │ │
│  │                                                                         │ │
│  │  restore(checkpointPath) {                                             │ │
│  │    metadata = loadMetadata(checkpointPath)                             │ │
│  │    newPod = k8sClient.createPod(metadata.podSpec)                      │ │
│  │    waitForContainer(newPod)                                            │ │
│  │                                                                         │ │
│  │    container = containerdClient.getContainer(newPod)                   │ │
│  │    killEntrypoint(container)                                           │ │
│  │                                                                         │ │
│  │    goCriu.restore(opts{                                                │ │
│  │      root: container.rootfs,                                           │ │
│  │      joinNs: container.namespaces,                                     │ │
│  │    })                                                                   │ │
│  │                                                                         │ │
│  │    cudaCheckpoint.restore(newPid)                                      │ │
│  │    cudaCheckpoint.unlock(newPid)                                       │ │
│  │  }                                                                      │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  Dependencies:                                                               │
│    - github.com/containerd/containerd (container management)               │
│    - github.com/checkpoint-restore/go-criu/v7 (process checkpoint)         │
│    - os/exec for cuda-checkpoint (no Go library)                           │
│    - k8s.io/client-go (pod management)                                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Implementation Plan

### Phase 1: Core Libraries (2-3 hours)
1. Add containerd client dependency
2. Add go-criu dependency
3. Implement container discovery via containerd API
4. Implement CRIU dump via go-criu

### Phase 2: Restore Logic (2-3 hours)
1. Implement namespace discovery
2. Implement CRIU restore with namespace injection
3. Test with simple container

### Phase 3: GPU Integration (1-2 hours)
1. Integrate cuda-checkpoint calls
2. Test with GPU container

### Phase 4: K8s Integration (1-2 hours)
1. Controller for CRD watching
2. Pod creation for restore
3. End-to-end test

---

## Key Files to Create/Modify

```
internal/
├── containerd/
│   └── client.go       # containerd Go client wrapper
├── criu/
│   └── manager.go      # go-criu wrapper
├── cuda/
│   └── checkpoint.go   # cuda-checkpoint exec wrapper
└── agent/
    ├── checkpoint.go   # Updated with libraries
    └── restore.go      # Updated with libraries
```

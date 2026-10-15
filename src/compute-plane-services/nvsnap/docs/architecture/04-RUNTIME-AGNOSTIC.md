# Container Runtime Agnostic Design

## Why Runtime Agnosticism Matters

NVSNAP must work with any CRI-compliant container runtime:

| Runtime | Version Churn | API Stability |
|---------|---------------|---------------|
| containerd | Frequent updates, API changes between 1.x and 2.x | Breaking changes possible |
| CRI-O | Tied to K8s releases | API evolves with K8s |
| Docker (via cri-dockerd) | Legacy, shim overhead | Additional complexity |

**Problem**: Depending on runtime APIs means:
- Tracking multiple API versions
- Different code paths for each runtime
- Breakage on runtime updates
- Cannot support new runtimes without code changes

**Solution**: Operate at the Linux process/namespace level.

## Linux Primitives We Use

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                   LINUX PRIMITIVES (Runtime Agnostic)                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  /proc filesystem                                                           │
│  ├── /proc/[pid]/cmdline      → Process command line                       │
│  ├── /proc/[pid]/environ      → Environment variables                      │
│  ├── /proc/[pid]/cgroup       → Cgroup membership (find pod)              │
│  ├── /proc/[pid]/ns/*         → Namespace references                       │
│  ├── /proc/[pid]/maps         → Memory mappings                           │
│  ├── /proc/[pid]/fd/          → Open file descriptors                      │
│  ├── /proc/[pid]/status       → Process status                            │
│  └── /proc/[pid]/task/        → Thread information                        │
│                                                                             │
│  Cgroups (v1 and v2)                                                       │
│  ├── /sys/fs/cgroup/          → Cgroup hierarchy                          │
│  ├── freezer controller       → Freeze/unfreeze processes                 │
│  └── Pod identification       → Extract pod UID from cgroup path          │
│                                                                             │
│  Namespaces                                                                 │
│  ├── pid namespace            → Process isolation                         │
│  ├── net namespace            → Network isolation                         │
│  ├── mnt namespace            → Mount isolation                           │
│  ├── ipc namespace            → IPC isolation                             │
│  └── user namespace           → User isolation                            │
│                                                                             │
│  Signals                                                                    │
│  ├── SIGSTOP                  → Freeze process                            │
│  └── SIGCONT                  → Resume process                            │
│                                                                             │
│  CRIU                                                                       │
│  └── Works with any container runtime                                      │
│      (operates on processes, not containers)                               │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Mapping Kubernetes to Linux Processes

### From Pod to Processes

```go
// illustrative pseudocode — discovery lives in internal/agent/

// We don't call container runtime APIs
// Instead, we use cgroup paths to find pod processes

func FindProcessesByPodUID(podUID string) ([]*Process, error) {
    // Kubernetes assigns cgroup paths like:
    // containerd: /kubepods/pod<uid>/cri-containerd-<container-id>
    // CRI-O:      /kubepods/pod<uid>/crio-<container-id>
    // Docker:     /kubepods/pod<uid>/docker-<container-id>
    
    // Pattern works for ALL runtimes:
    pattern := fmt.Sprintf("pod%s", podUID)
    
    var processes []*Process
    
    // Scan all processes
    entries, _ := os.ReadDir("/proc")
    for _, entry := range entries {
        pid, err := strconv.Atoi(entry.Name())
        if err != nil {
            continue
        }
        
        // Read process cgroup
        cgroupPath := fmt.Sprintf("/proc/%d/cgroup", pid)
        data, err := os.ReadFile(cgroupPath)
        if err != nil {
            continue
        }
        
        // Check if this process belongs to our pod
        if strings.Contains(string(data), pattern) {
            proc, _ := getProcessInfo(pid)
            processes = append(processes, proc)
        }
    }
    
    return processes, nil
}

// Extract pod UID from any cgroup path
func ExtractPodUID(cgroupPath string) string {
    // Works for containerd, CRI-O, Docker
    // Pattern: /kubepods[.slice]/[burstable|besteffort]/pod<uid>
    
    re := regexp.MustCompile(`pod([a-f0-9-]{36})`)
    matches := re.FindStringSubmatch(cgroupPath)
    if len(matches) > 1 {
        return matches[1]
    }
    return ""
}
```

### Getting Container Information Without Runtime API

```go
// illustrative pseudocode — discovery lives in internal/agent/

type ContainerInfo struct {
    ID          string
    Name        string
    PID         int
    Namespaces  Namespaces
    Cgroup      string
    Environment map[string]string
    Mounts      []Mount
    GPUDevices  []string
}

func GetContainerInfo(pid int) (*ContainerInfo, error) {
    info := &ContainerInfo{PID: pid}
    
    // Container ID from cgroup path
    cgroupData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
    info.Cgroup = string(cgroupData)
    info.ID = extractContainerID(info.Cgroup)
    
    // Environment variables (includes container name, pod info)
    envData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
    info.Environment = parseEnviron(envData)
    info.Name = info.Environment["HOSTNAME"] // Often set to container name
    
    // Namespaces
    info.Namespaces = readNamespaces(pid)
    
    // Mounts (to find volumes)
    mountsData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/mounts", pid))
    info.Mounts = parseMounts(mountsData)
    
    // GPU devices
    fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
    for _, fd := range fds {
        link, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
        if strings.HasPrefix(link, "/dev/nvidia") {
            info.GPUDevices = append(info.GPUDevices, link)
        }
    }
    
    return info, nil
}

func extractContainerID(cgroupPath string) string {
    // containerd: cri-containerd-<64-char-hex>
    // CRI-O:      crio-<64-char-hex>
    // Docker:     docker-<64-char-hex>
    
    patterns := []string{
        `cri-containerd-([a-f0-9]{64})`,
        `crio-([a-f0-9]{64})`,
        `docker-([a-f0-9]{64})`,
    }
    
    for _, pattern := range patterns {
        re := regexp.MustCompile(pattern)
        if matches := re.FindStringSubmatch(cgroupPath); len(matches) > 1 {
            return matches[1]
        }
    }
    
    return ""
}
```

## Pause/Resume Without Runtime API

### Method 1: Signals (Simple)

```go
// illustrative pseudocode — process control lives in internal/agent/

func PauseProcess(pid int) error {
    return syscall.Kill(pid, syscall.SIGSTOP)
}

func ResumeProcess(pid int) error {
    return syscall.Kill(pid, syscall.SIGCONT)
}

// Pause all threads
func PauseProcessGroup(pgid int) error {
    return syscall.Kill(-pgid, syscall.SIGSTOP)
}
```

### Method 2: Cgroup Freezer (More Reliable)

```go
// illustrative pseudocode — cgroup freezer lives in internal/agent/

type CgroupFreezer struct {
    version int // 1 or 2
}

func (f *CgroupFreezer) Freeze(cgroupPath string) error {
    if f.version == 2 {
        // cgroup v2: write to cgroup.freeze
        freezePath := filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.freeze")
        return os.WriteFile(freezePath, []byte("1"), 0644)
    } else {
        // cgroup v1: write to freezer.state
        freezePath := filepath.Join("/sys/fs/cgroup/freezer", cgroupPath, "freezer.state")
        return os.WriteFile(freezePath, []byte("FROZEN"), 0644)
    }
}

func (f *CgroupFreezer) Thaw(cgroupPath string) error {
    if f.version == 2 {
        freezePath := filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.freeze")
        return os.WriteFile(freezePath, []byte("0"), 0644)
    } else {
        freezePath := filepath.Join("/sys/fs/cgroup/freezer", cgroupPath, "freezer.state")
        return os.WriteFile(freezePath, []byte("THAWED"), 0644)
    }
}

func (f *CgroupFreezer) IsFrozen(cgroupPath string) bool {
    var statePath string
    if f.version == 2 {
        statePath = filepath.Join("/sys/fs/cgroup", cgroupPath, "cgroup.freeze")
        data, _ := os.ReadFile(statePath)
        return strings.TrimSpace(string(data)) == "1"
    } else {
        statePath = filepath.Join("/sys/fs/cgroup/freezer", cgroupPath, "freezer.state")
        data, _ := os.ReadFile(statePath)
        return strings.TrimSpace(string(data)) == "FROZEN"
    }
}
```

## Kubernetes Integration Without CRI Dependency

### Watch Pods, Not Containers

```go
// internal/controller/pod_watcher.go

// We watch Kubernetes Pod resources, not container runtime events
// This is Kubernetes-version-agnostic (Pod API is stable)

func (c *Controller) watchPods(ctx context.Context) error {
    // Use Kubernetes client to watch pods
    watcher, err := c.client.CoreV1().Pods("").Watch(ctx, metav1.ListOptions{
        LabelSelector: "nvsnap.io/enabled=true",
    })
    if err != nil {
        return err
    }
    
    for event := range watcher.ResultChan() {
        pod := event.Object.(*corev1.Pod)
        
        switch event.Type {
        case watch.Added, watch.Modified:
            if pod.Status.Phase == corev1.PodRunning {
                c.registerPod(pod)
            }
        case watch.Deleted:
            c.unregisterPod(pod)
        }
    }
    
    return nil
}

func (c *Controller) registerPod(pod *corev1.Pod) {
    // Find processes by pod UID (runtime-agnostic)
    processes, err := FindProcessesByPodUID(string(pod.UID))
    if err != nil {
        c.logger.Error("failed to find processes", zap.Error(err))
        return
    }
    
    // Register with agent for checkpoint capability
    c.agent.RegisterWorkload(&Workload{
        PodUID:    string(pod.UID),
        Namespace: pod.Namespace,
        Name:      pod.Name,
        Processes: processes,
    })
}
```

### Mutating Webhook (Also Runtime-Agnostic)

```go
// internal/webhook/mutating.go

// Inject LD_PRELOAD via pod mutation
// This works with ANY container runtime

func (w *Webhook) mutatePod(pod *corev1.Pod) *corev1.Pod {
    // Check if NVSNAP is enabled for this pod
    if pod.Labels["nvsnap.io/enabled"] != "true" {
        return pod
    }
    
    // Add init container to ensure library is available
    pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
        Name:  "nvsnap-init",
        Image: "ghcr.io/nvsnap/nvsnap-lib:latest",
        Command: []string{
            "cp", "/libnvsnap.so", "/opt/nvsnap/lib/",
        },
        VolumeMounts: []corev1.VolumeMount{
            {Name: "nvsnap-lib", MountPath: "/opt/nvsnap/lib"},
        },
    })
    
    // Add volume for library
    pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
        Name: "nvsnap-lib",
        VolumeSource: corev1.VolumeSource{
            EmptyDir: &corev1.EmptyDirVolumeSource{},
        },
    })
    
    // Inject LD_PRELOAD into all GPU containers
    for i := range pod.Spec.Containers {
        container := &pod.Spec.Containers[i]
        
        // Check if container requests GPU
        if hasGPURequest(container) {
            container.Env = append(container.Env, corev1.EnvVar{
                Name:  "LD_PRELOAD",
                Value: "/opt/nvsnap/lib/libnvsnap.so",
            })
            container.Env = append(container.Env, corev1.EnvVar{
                Name:  "NVSNAP_AGENT_SOCKET",
                Value: "/var/run/nvsnap/agent.sock",
            })
            container.VolumeMounts = append(container.VolumeMounts,
                corev1.VolumeMount{Name: "nvsnap-lib", MountPath: "/opt/nvsnap/lib", ReadOnly: true},
                corev1.VolumeMount{Name: "nvsnap-socket", MountPath: "/var/run/nvsnap"},
            )
        }
    }
    
    // Add socket volume (from host)
    pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
        Name: "nvsnap-socket",
        VolumeSource: corev1.VolumeSource{
            HostPath: &corev1.HostPathVolumeSource{
                Path: "/var/run/nvsnap",
                Type: hostPathDirectoryOrCreate(),
            },
        },
    })
    
    return pod
}
```

## Testing Runtime Agnosticism

### Matrix Testing

```yaml
# Illustrative matrix-testing workflow (example, not shipped in this repo)

name: Runtime Compatibility Matrix

on: [push, pull_request]

jobs:
  test:
    strategy:
      matrix:
        runtime:
          - name: containerd
            version: ["1.6", "1.7", "2.0"]
          - name: crio
            version: ["1.27", "1.28", "1.29"]
        kubernetes:
          - "1.27"
          - "1.28"
          - "1.29"
          - "1.30"
    
    runs-on: ubuntu-latest
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup cluster
        run: |
          # Create kind cluster with specified runtime
          ./scripts/create-cluster.sh \
            --runtime ${{ matrix.runtime.name }} \
            --runtime-version ${{ matrix.runtime.version }} \
            --k8s-version ${{ matrix.kubernetes }}
      
      - name: Run tests
        run: |
          make test-integration
          
          # Verify no runtime-specific code paths used
          make verify-runtime-agnostic
```

### Runtime Verification Test

```go
// test/integration/runtime_agnostic_test.go

func TestRuntimeAgnostic(t *testing.T) {
    // This test verifies we don't make any runtime-specific API calls
    
    // 1. Deploy test pod
    pod := deployTestPod(t)
    
    // 2. Find processes using our runtime-agnostic method
    processes, err := FindProcessesByPodUID(string(pod.UID))
    require.NoError(t, err)
    require.NotEmpty(t, processes)
    
    // 3. Get container info without runtime API
    for _, proc := range processes {
        info, err := GetContainerInfo(proc.PID)
        require.NoError(t, err)
        require.NotEmpty(t, info.ID)
    }
    
    // 4. Pause/resume without runtime API
    for _, proc := range processes {
        require.NoError(t, PauseProcess(proc.PID))
        require.True(t, isProcessStopped(proc.PID))
        require.NoError(t, ResumeProcess(proc.PID))
        require.False(t, isProcessStopped(proc.PID))
    }
    
    // 5. Verify we didn't call any container runtime
    // (mock/intercept socket calls to verify)
    assertNoRuntimeAPICalls(t)
}

func assertNoRuntimeAPICalls(t *testing.T) {
    // Check no connections to runtime sockets
    sockets := []string{
        "/run/containerd/containerd.sock",
        "/run/crio/crio.sock",
        "/var/run/docker.sock",
    }
    
    for _, socket := range sockets {
        // Use eBPF or strace to verify no calls
        // Or check our own connection logs
    }
}
```

## Comparison: With vs Without Runtime API

| Operation | With Runtime API | Our Approach |
|-----------|------------------|--------------|
| Find container PIDs | `ctr tasks ls` | `/proc/*/cgroup` + pattern match |
| Get container env | `crictl inspect` | `/proc/[pid]/environ` |
| Get container mounts | `crictl inspect` | `/proc/[pid]/mounts` |
| Pause container | `crictl pause` | `SIGSTOP` or cgroup freezer |
| Resume container | `crictl unpause` | `SIGCONT` or cgroup thaw |
| Container namespace | `crictl inspect` | `/proc/[pid]/ns/*` |
| GPU devices | Runtime-specific | `/proc/[pid]/fd/*` → `/dev/nvidia*` |
| Container ID | Runtime API | Extract from cgroup path |

## Edge Cases

### 1. Rootless Containers

```go
// Handle user namespaces
func (d *Discovery) handleRootless(pid int) (*Process, error) {
    // Check if in user namespace
    userNS, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/user", pid))
    hostUserNS, _ := os.Readlink("/proc/1/ns/user")
    
    if userNS != hostUserNS {
        // Running in user namespace (rootless)
        // Need to enter namespace for some operations
        return d.discoverInUserNS(pid)
    }
    
    return d.discoverNormal(pid)
}
```

### 2. Nested Containers (Docker-in-Docker)

```go
// Handle nested container scenarios
func (d *Discovery) findRealPod(pid int) (string, error) {
    cgroupData, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
    
    // May have multiple pod patterns in nested scenario
    // Take the innermost one
    patterns := regexp.MustCompile(`pod([a-f0-9-]{36})`).FindAllStringSubmatch(string(cgroupData), -1)
    
    if len(patterns) > 0 {
        // Last match is innermost
        return patterns[len(patterns)-1][1], nil
    }
    
    return "", fmt.Errorf("no pod UID found")
}
```

### 3. Sidecar Containers

```go
// Handle pods with multiple containers
func (d *Discovery) classifyContainers(processes []*Process) (*PodContainers, error) {
    result := &PodContainers{
        Main:     []*Process{},
        Sidecars: []*Process{},
    }
    
    for _, p := range processes {
        // GPU containers are main workloads
        if hasGPU(p) {
            result.Main = append(result.Main, p)
        } else {
            // Non-GPU are likely sidecars (envoy, logging, etc.)
            result.Sidecars = append(result.Sidecars, p)
        }
    }
    
    return result, nil
}
```

## Benefits of This Approach

1. **Zero Runtime Dependency**: Works with containerd, CRI-O, Docker, and future runtimes
2. **Version Agnostic**: Linux primitives don't change
3. **Less Code**: Single code path instead of runtime-specific branches
4. **More Reliable**: Fewer external dependencies to fail
5. **Easier Testing**: Can test on any runtime without code changes
6. **Future Proof**: New runtimes automatically supported

## Summary

NVSNAP achieves container runtime agnosticism by:

1. Using `/proc` filesystem instead of runtime APIs
2. Leveraging cgroup paths for pod/container identification
3. Using kernel primitives (signals, namespaces) for process control
4. Working with CRIU which itself is runtime-agnostic
5. Integrating with Kubernetes at the Pod level, not container level

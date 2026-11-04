# node-tuning

A DaemonSet that raises the Linux kernel's inotify limits on every cluster node at startup.

## Why this exists

### The problem

Fluent Bit uses the `tail` input plugin to watch container log files via the Linux `inotify` subsystem. Each log file it monitors consumes an inotify instance. The Linux kernel enforces a per-user limit on how many inotify instances can be open at once, controlled by:

- `fs.inotify.max_user_instances` - maximum number of inotify queues one user can open (default: `128`)
- `fs.inotify.max_user_watches` - maximum number of files that can be watched in total (default: `8192`)

On a Kubernetes node with many pods each writing logs, Fluent Bit exhausts `max_user_instances` immediately on startup and crashes with:

```text
[error] [in_tail/tail_fs_inotify.c] errno=24] Too many open files
[error] failed initialize input tail.0
[error] [engine] input initialization failed
```

This manifests as a `CrashLoopBackOff` on the Fluent Bit DaemonSet.

### Why this is worse on macOS (k3d)

k3d runs Kubernetes nodes as Docker containers inside a Linux VM managed by Docker Desktop or Rancher Desktop. That VM starts with the kernel default of `max_user_instances=128`. Since all containers on a host share the host kernel, every k3d node inherits this low limit.

The fix, writing a higher value to `/proc/sys/fs/inotify/max_user_instances`, must be applied to the VM's kernel. On macOS you cannot edit the VM's config files directly (the filesystem is read-only and ephemeral), so the fix must be applied at runtime via a privileged container.

### Why a pod-level sysctl doesn't work

The obvious Kubernetes-native solution would be to set the sysctl in Fluent Bit's pod spec:

```yaml
securityContext:
  sysctls:
    - name: fs.inotify.max_user_instances
      value: "8192"
```

This requires the kubelet to know that `fs.inotify.*` is a namespaced sysctl (i.e. safe to scope to an individual pod). While the Linux kernel has supported inotify namespacing since kernel 5.x, the Kubernetes kubelet has never added `fs.inotify.*` to its known-namespaced sysctl list. As of the latest Kubernetes source, the list in `component-helpers/node/util/sysctl/namespace.go` only covers `kernel.shm*`, `kernel.msg*`, `kernel.sem`, `net.*`, and `fs.mqueue.*`.

Attempting to use `--kubelet-arg=allowed-unsafe-sysctls=fs.inotify.max_user_instances` (via k3d's `options.k3s.extraArgs`) causes the kubelet to refuse to start entirely:

```text
Error: failed to run Kubelet: failed to create kubelet: the sysctls "fs.inotify.max_user_instances" are not known to be namespaced
```

This is a hard limitation in Kubernetes, not a version issue or a configuration issue.

### Why not just run `docker run --privileged` once on the VM?

The one-liner approach, running a privileged container from the host to write directly to the Docker VM's `/proc/sys`, does work as an immediate fix:

```bash
docker run --rm --privileged --net=host --pid=host alpine \
  sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576
```

But it has two fundamental problems:

1. It doesn't survive restarts. The Docker VM (whether Docker Desktop or Rancher Desktop) resets kernel parameters every time it restarts. After a machine reboot or a Docker restart, the limits drop back to 128 and Fluent Bit starts crashing again. There is no supported way to persist sysctl changes across restarts on macOS. The VM's root filesystem is read-only and recreated from a clean image on each boot.

2. It's not in the repository. A one-time manual command only exists in the memory of whoever ran it. The next developer to clone this repo and spin up the cluster will hit the same crash with no indication of why or how to fix it. The fix needs to live in the codebase alongside the cluster definition.

The DaemonSet solves both problems: it runs automatically every time the cluster starts (including after restarts) and it is version-controlled alongside everything else.

## How this DaemonSet works

The DaemonSet runs on every node (including the server node, via the blanket `tolerations`) and uses an init container to apply the sysctl fix before the main container starts:

1. The init container runs `sysctl -w` with `privileged: true` and `hostPID: true`, giving it access to write to the node's `/proc/sys`.
2. Since all pods on a node share the same Linux user namespace (Kubernetes pod user namespace isolation is still alpha), writing the sysctl here raises the limit for the entire node. Fluent Bit inherits it automatically.
3. The init container exits after applying the fix. The main container is a no-op `pause` image that keeps the pod alive, which ensures Kubernetes will re-run the init container if a node restarts.

## Deployment

This manifest is deployed automatically via k3s's built-in auto-deploy mechanism. k3s watches `/var/lib/rancher/k3s/server/manifests/` on the server node and applies any YAML files it finds there on startup. No `kubectl apply` is required.

The file is mounted into the server node by `k3d-config.yaml`:

```yaml
volumes:
  - volume: ${PWD}/apps/node-tuning/node-tuning.yaml:/var/lib/rancher/k3s/server/manifests/node-tuning.yaml
    nodeFilters:
      - server:*
```

This means the DaemonSet is applied before any Makefile targets run, and before any other workloads are deployed. It requires no explicit step in the cluster setup pipeline.

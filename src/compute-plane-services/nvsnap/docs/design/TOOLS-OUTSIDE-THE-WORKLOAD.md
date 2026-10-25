<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Privileged tools outside the workload's filesystem

Status: proposed. Scope: the agent's capture and restore (`checkpoint_v2.go`, `pidns_capture.go`, `restore_v2.go`,
`restore.go`, `rootfs_diff.go`, `gpushare.go`), the node bundle, and the gpushare tool.

## Problem

The agent runs some tools as host root inside a workload's mount namespace:

| Tool | Where it runs today | Why there |
|---|---|---|
| CRIU dump (in-namespace path) | workload's mount, pid, net, ipc, uts namespaces | sees the container's mounts natively |
| CRIU restore, and the pid-namespace restore helper | placeholder's namespaces | same |
| tar, mkdir (rootfs and mount replay) | placeholder's mount namespace | writes through the placeholder's overlay |
| `nvsnap-gpu-suspend` | workload's namespaces | loads the workload's libcuda, reaches its GPU devices |

Since `aef252280` the binaries come from the read-only node bundle, verified before each run. But a dynamically linked
program also reads its loader's configuration and loads shared libraries from the filesystem it runs in, and the
workload controls that filesystem. A read-only bundle mount does not cover what the loader reads.

The pid-namespace dump already avoids this: the agent runs its own CRIU from its own filesystem, with `--root` pointing
into the container, and enters only the pod's network namespace.

## Proposal

1. **CRIU runs from the agent's filesystem, for every dump and restore.** The pid-namespace path becomes the only dump
   path (it dumps the container's whole pid namespace, which is also what fixes the pid collisions the in-namespace
   path works around). Restore does the same: the agent's CRIU with `--root` into the placeholder, entering only the
   namespaces CRIU needs, never the placeholder's mount namespace for the binary itself.
2. **The replays (rootfs diff, replay mounts, unix-socket directories) run in the agent, in Go**, through a directory
   opened under the placeholder's root (`confined_fs.go`): `archive/tar` extraction into an `os.Root`, no external tar or
   mkdir in the placeholder.
3. **`nvsnap-gpu-suspend` runs as the workload, not as root.** It has to load the workload's libcuda, so it stays in the
   workload's namespaces, but with the workload process's uid and gid, its capability bounding set as a ceiling, only
   `CAP_SYS_PTRACE` effective (what the tool needs to freeze threads; the GPU workloads captured today already have it),
   and `no_new_privs`. Whatever it loads then runs with the workload's own rights.
4. **Namespaces are entered by descriptor.** The agent opens `/proc/<pid>/ns/*` once, checks them (the user namespace
   is the initial one; the mount namespace is the one it verified), and enters through those descriptors, so a later
   change of the process's namespaces is not followed.

With 1 and 2, the bundle mount at `/criu-bundle` is no longer needed for CRIU or the replays; it stays for the GPU tool
until 3 lands, then can go.

### Pods that share one pid namespace

A pod with `shareProcessNamespace` has one pid namespace for all its containers, whose init is the pause process: a
container's own process is not pid 1 there, so its pid namespace cannot be dumped as its own. Such a container is dumped
as its own process tree (a subtree), still with the agent's CRIU: the agent enters only the pod's pid and network
namespaces, by descriptor, and a helper (`pidns-dump-exec`) mounts a procfs of that pid namespace in a private copy of
the agent's mount namespace, where CRIU finds the tree by its pids there. `--root` reaches the container's root through
the node's `/proc`. The checkpoint's marker records the subtree and its root's pid.

Its restore recreates the tree at its dumped pids in the placeholder's own (shared) pid namespace: the placeholder bumps
`ns_last_pid` clear of them, and the restore waits until no placeholder task holds a pid the checkpoint recreates (the
same wait the in-namespace restore had). Ending the runtime's exec sessions before a dump only touches processes of the
container's own cgroup: the pause process and the other containers, each the child of its own shim, are not the
container's sessions.

### The placeholder's root

A bind mount cannot take its source from another mount namespace: `mount --bind /proc/<pid>/root` (and `open_tree`
with `OPEN_TREE_CLONE`) from outside fail with `EINVAL` (checked on the GB300 nodes, kernel 6.17). The node bundle's
static agent binary, run in the placeholder's mount namespace (as it already is to bind the capture's listening
sockets), clones the placeholder's mount tree there (`open_tree`) and passes it back over a socket pair; the restore
helper attaches it with `move_mount`.

## What it costs

- The in-namespace dump path goes. It is the default today; the pid-namespace path has been validated on single-pod
  and two-pod captures, not yet on every workload in the regression set.
- The GPU tool needs a check from WrapCore that it works with only `CAP_SYS_PTRACE` as the workload's user (the driver
  checkpoint API on processes of the same user, the control socket's peer check).
- Restore from the agent's side needs CRIU to restore into the placeholder's mount namespace with the binary outside
  it: CRIU supports `--root`; the pid-namespace restore helper changes accordingly.

## Tests

- Every regression workload (single-GPU vLLM, SGLang, TRT-LLM; multi-GPU; Kimi-K3 two-node) captures and restores
  through the pid-namespace path, and a pod that shares one pid namespace through the subtree path.
- A placeholder whose image carries loader configuration or libraries of its own: the restore loads none of them
  (CRIU and the replays run from the agent's filesystem).
- The GPU tool as the workload's user: suspend and resume of a TP=4 vLLM, and of a two-node fabric session.
- A namespace change between the check and the run is refused.

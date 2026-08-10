# Plan A: Clean CRIU Dump via RPC + Explicit ExtMnt (no skip hacks)

## Problem Statement

Our CRIU dump currently fails on Kubernetes GPU nodes with NVIDIA driver mounts that have mount propagation relationships (e.g. `/proc/driver/nvidia/gpus/...`) that CRIU cannot validate from inside the container mount namespace:

- `mnt: ... has unreachable sharing ...`

Historically, this was bypassed via the custom flag `--skip-mnt-ns`, which suppresses mount propagation validation. We want a **clean**, upstream-aligned approach with **no skip hacks**.

## Key Insight

The clean approach used by `k8s-runc-bypass` is:

- **Do not use `external mnt[]` wildcard alone.**
- Instead, enumerate mounts from `/proc/<pid>/mountinfo` and provide CRIU **explicit external mount mappings** (`ExtMnt`) for every mountpoint CRIU will see.
- Run CRIU via **go-criu RPC** so we can provide `ExtMnt`, `ExtUnixSk`, `ExtMasters`, timeouts, ghost limits, etc.

This prevents CRIU from trying to reason about / recreate a complex mount hierarchy and avoids the NVIDIA mount propagation validation failure.

## Plan A Deliverables

### A1) Implement mountinfo introspection (in nvsnap)

Create a small internal package:

- Input: container init PID (host PID)
- Source: `/host/proc/<pid>/mountinfo` (fallback `/proc/<pid>/mountinfo`)
- Output: a list of mountpoints (strings), plus helpers to build CRIU `ExtMnt` mappings:
  - `key = mountpoint`
  - `val = mountpoint`

Additionally, persist the dump-time mountpoint list into checkpoint `metadata.json`
so restore can generate matching `ExtMnt` maps (avoid “no mapping for mountpoint”).

Notes:
- Deduplicate mountpoints.
- Preserve ordering stable (sorted by mountpoint length ascending is safest for restore order, but CRIU consumes `ExtMnt` as a set; we can sort lexicographically).

### A2) Add CRIU Dump manager using go-criu RPC (v8 fork)

Implement a new dump path that matches restore-entrypoint’s CRIU model:

- Build `criurpc.CriuOpts` with:
  - `Pid`
  - `ImagesDirFd`
  - `LogFile = dump.log`
  - `Root = <rootfs>` (see A3)
  - `ManageCgroups=IGNORE`
  - `ExtUnixSk=true`
  - `ExtMasters=true` (if supported by our CRIU build)
  - `ExtMnt = explicit maps from A1`
  - `External = ["net[]", ...]` (see A4)
  - `TcpEstablished`, `TcpClose`
  - `LinkRemap`, `FileLocks`
  - `SkipFsnotify`, `SkipInFlight`
  - `Timeout` (GPU workloads)
  - `GhostLimit` (GPU workloads)
  - `ConfigFile` for options not exposed via RPC if needed (e.g. plugin `libdir`)

Keep the existing CLI dump implementation as a **temporary fallback** (config controlled) until we prove stability.

### A3) Determine correct Root for dump

We should pass a root that matches the container filesystem view.

Options:
- If `ContainerInfo.RootFS` is an absolute path and exists, use it.
- Else, use `/proc/<pid>/root` (or `/host/proc/<pid>/root`) as the Root path.

This aligns with how CRIU expects to resolve file paths for the target process.

### A4) Network namespace externalization (follow-up in Plan A)

To avoid CRIU attempting to restore veth devices/topology:

- Dump: mark netns as external `net[<inode>]:extNetNs` (requires reading netns inode).
- Restore: pass `InheritFd` mapping for `extNetNs`.

This is standard and matches k8s-runc-bypass.

We can implement A4 after A1/A2 unblock dump.

### A5) Deterministic checkpoint cleanup (always)

Before creating a new checkpoint for a given `(namespace, podName)`:
- Delete existing checkpoint directories matching `<pod>-<ns>-*` under the checkpoint root.

This keeps host storage clean and avoids confusion during iteration.

## Success Criteria

- CRIU dump succeeds on GPU nodes without `--skip-mnt-ns`.
- Restore continues to use `MntnsCompatMode` + explicit `ExtMnt`.
- No `skip-missing-*` behaviour masking FD problems.
- End-to-end vLLM restore reaches inference without worker IPC failures.

## Non-Goals (for Plan A)

- Removing dead custom flags from the CRIU fork itself (can be done after stability).
- Reworking uvloop shims or io_uring patches (separate tracks).


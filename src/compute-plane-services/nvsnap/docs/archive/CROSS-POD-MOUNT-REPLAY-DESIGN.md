# Cross-Pod Mount Replay — Design

**Status:** draft, awaiting review
**Branch:** `feat/cross-pod-mount-replay`
**Author:** 2026-05-01
**Predecessor docs:** `docs/CROSS-POD-RESTORE-DESIGN.md`, `docs/AGENT-DRIVEN-RESTORE-STATUS.md`

## Problem

Today's agent-driven cross-pod restore replays exactly **one** filesystem from the source pod into the placeholder pod: the container's overlayfs **upperdir**, mirrored via `mirrorOverlayDir` into `<checkpointDir>/rootfs-diff`, then untar'd into the placeholder's mntns by `mirrorIntoMntns`. Every other writable mount that the source container had — `/dev/shm`, `/tmp`, `/var/run`, custom emptyDir or hostPath volumes — is left **empty** in the placeholder.

This works as long as the workload only keeps state in the rootfs. Each new workload class that uses a non-rootfs mount surfaces a new restore failure:

- vllm-small, sglang-small, vllm-8b TP=1, sglang-8b, trtllm-small (single-GPU): no non-rootfs writable state — work today.
- vllm-8b TP=2 (multi-GPU): NCCL/PSM puts inter-rank SHM segments in `/dev/shm/psm_*`. CRIU restore fails with `Can't open file dev/shm/psm_e4c81439: No such file or directory`, rank-1 process exits status=1, restore aborts.
- The next workload that uses `/tmp` for runtime state (JIT caches, lockfiles), or that writes into a custom mounted volume, will break the same way.

Fixing each mount as a one-off is whack-a-mole. The structural problem is that **the cross-pod restore mechanism captures one mount but the source pod has many writable mounts and the workload may use any of them**.

## Why the current code is incomplete

`internal/agent/checkpoint.go` (around line 1779) resolves two things:

```go
sourceUpperdir, _    := mountinfo.ResolveOverlayUpperdir(int(containerInfo.PID))
sourceMountPoints, _ := mountinfo.NonRootMountPoints(int(containerInfo.PID))
```

`sourceMountPoints` is used **as an exclude list** by `mirrorOverlayDir` (passed via `excludeArgsForMountpoints`) — i.e. anything that's a separate mount on top of the rootfs is *removed* from the tar so we don't include K8s identity binds and CDI-injected nvidia paths in the diff. Correct for the rootfs mirror. But it also means every separate-mount filesystem is silently dropped from the dump.

There's no parallel "for each non-rootfs writable mount, snapshot its contents into the checkpoint" pass. Restore similarly has nothing that replays writable tmpfs/emptyDir contents into the placeholder's mounts.

`shouldSkipMount` in `internal/agent/checkpoint_plan_a.go` is currently a 2-way classifier (skip vs. dump-as-external) for CRIU's `--ext-mount-map` flag, not for content replay. We need a 3-way classifier.

## Proposed approach

Extend the existing mountinfo flow into a **3-way mount classifier** and add a new "replay" path that snapshots writable container-internal mounts at checkpoint and untars them into the placeholder pre-restore.

**Scope discipline (decided 2026-05-02): use an explicit allowlist, not auto-detect.** The classifier framework is generic so future mounts are a config change, not a code change — but the default `replay` set is just `["/dev/shm"]`. This avoids accidental "kitchen sink" snapshots (e.g. a multi-GB inductor JIT cache mounted at `/tmp`, or a 50 GB emptyDir at `/data`) while still solving the structural problem. New entries are added to the allowlist as new workloads need them.

### Classification

For each entry in `/proc/<source-pid>/mountinfo`, classify as:

| Class | Examples | Treatment |
|---|---|---|
| **skip** | `/dev/nvidia*`, `/proc`, `/sys`, `/sys/fs/cgroup`, `/etc/hostname`, `/etc/hosts`, `/etc/resolv.conf`, `/run/.containerenv`, `/run/secrets/kubernetes.io/...`, CDI bind injections (`/usr/bin/nvidia-*`, `/usr/lib/firmware/nvidia/...`, `libnvidia-*.so`, `libcuda*.so`), `/nvsnap-lib`, **and anything not on the replay allowlist** | already handled today; no change |
| **rootfs** | the `overlay` mount at `/` | already handled by `mirrorOverlayDir` over `sourceUpperdir`; no change |
| **replay** | mountpoints on the configured allowlist (default: `/dev/shm`) that are read-write and not a virtual fs | **NEW**: snapshot at checkpoint, untar at restore |

### Decision rules for "replay"

A mount is `replay`-class iff **all** of:

1. Mountpoint is on the configured allowlist. **Default allowlist: `["/dev/shm"]`.**
2. Not in `skip` set above.
3. Not the rootfs (`/`).
4. Mounted **read-write** (mountinfo opts contains `rw`).
5. NOT a `proc`/`sysfs`/`cgroup`/`cgroup2`/`devpts`/`mqueue`/`bpf`/`debugfs`/`tracefs`/`fusectl`/`securityfs` mount (those are virtual).

The allowlist is surfaced as an agent config knob (env var `NVSNAP_REPLAY_MOUNTS`, comma-separated paths) so adding `/tmp` etc. later is a config change without rebuilding the agent.

### Capture (checkpoint side)

In `internal/agent/checkpoint.go` after the existing `mirrorOverlayDir` block:

```go
// Pseudocode
for _, mi := range parseMountinfo(sourcePid) {
    if classifyMount(mi) != mountClassReplay { continue }
    snapshotPath := filepath.Join(checkpointDir, "mounts", sanitize(mi.MountPoint) + ".tar")
    if err := tarMount(sourcePid, mi.MountPoint, snapshotPath); err != nil {
        log.Warnf("failed to snapshot mount %s: %v", mi.MountPoint, err)
    }
}
```

`tarMount` runs `tar -C /proc/<source-pid>/root/<mp> -cf <out> --xattrs --acls --numeric-owner --sparse --one-file-system .` — same invocation pattern as `mirrorOverlayDir`. We use `/proc/<pid>/root/<mp>` (not nsenter) because tar is a file-system operation that the agent can do unprivileged from its own mntns by reading through the source's procfs root link.

The classification + paths get written into the checkpoint metadata so restore knows what to replay:

```go
metadata.ReplayMounts = []ReplayMount{
    {Path: "/dev/shm", FsType: "tmpfs", Tarball: "mounts/dev_shm.tar"},
    ...
}
```

### Replay (restore side)

In `internal/agent/restore.go` before the CRIU restore call:

```go
// Pseudocode
for _, rm := range metadata.ReplayMounts {
    if err := untarIntoMntns(placeholderPid, rm.Path, filepath.Join(checkpointDir, rm.Tarball)); err != nil {
        log.Warnf("failed to replay mount %s: %v", rm.Path, err)
    }
}
```

`untarIntoMntns` pipes `tar -cf - -C <checkpointDir>/mounts/<x>.tar` into `nsenter -m -t <placeholder-pid> -- tar -C <mp> -xf - --xattrs --acls --numeric-owner` — same pattern as `mirrorIntoMntns` in `rootfs_diff.go`.

The placeholder is expected to **already have** a mount at the same path (e.g., a `/dev/shm` tmpfs from K8s default, or a matching emptyDir volumeMount). We extract INTO that mount; we do not create new mounts. This keeps us on the right side of "the placeholder is a normal pod, not a CRIU re-creation".

If the placeholder doesn't have the mount at the expected path, log a warning and skip that snapshot. CRIU will then fail with a clear "file not found" error pointing at the missing mount, which is a more diagnostic failure than today's silent missing state.

### Where it lands in the codebase

- `internal/agent/checkpoint_plan_a.go` — extend `shouldSkipMount` into `classifyMount(mp, fsType, opts) MountClass` returning `skip | rootfs | replay`. Keep `shouldSkipMount` as a thin wrapper for backwards compat with `buildDumpExtMnt`.
- `internal/agent/rootfs_diff.go` — add `tarMount(srcPid int, mp string, out string) error` and `untarIntoMntns(placeholderPid int, mp string, tarball string) error`. They share the tar-pipe plumbing already in `mirrorOverlayDir`/`mirrorIntoMntns`.
- `internal/agent/checkpoint.go` — after `mirrorOverlayDir` call, iterate `replay`-class mounts and snapshot each. Persist the list in the checkpoint metadata.
- `internal/agent/restore.go` — before CRIU restore (after the existing `mirrorIntoMntns` call), iterate metadata's `ReplayMounts` and replay each.
- `pkg/metadata/*.go` (or wherever checkpoint metadata lives) — add `ReplayMounts []ReplayMount`.

## Edge cases & non-goals

**Handled by design**:
- Multiple mounts with overlapping paths (e.g., `/dev/shm` and `/dev/shm/sub`): tar handles them naturally; we snapshot the deepest mount last and extract in the same order.
- `/dev/shm` files with `O_TMPFILE` / unlinked-but-open: not in the tar, but those FDs are restored by CRIU's regular VMA-and-FD machinery. Only NAMED files in the tmpfs need to travel.
- Symlinks within the snapshot: tar preserves them.
- Sparse files (CUDA mmaps, semaphore arenas): `--sparse` preserves holes.

**Explicit non-goals for this PR**:
- **Live updates between checkpoint and restore**: if the source pod is still running and writes to `/dev/shm` after we snapshot, those writes are lost. This is the same property as the rootfs upperdir mirror today.
- **Mounts the placeholder doesn't have**: we don't synthesize new mounts in the placeholder; we replay into existing ones. If the source had a custom volumeMount the placeholder yaml lacks, the user gets a warning.
- **Cross-node**: same restriction as today — checkpoint and restore on the same node. Cross-node is a separate problem (snapshot transport, image streaming).
- **Encrypted/sensitive content** (e.g., `/run/secrets/`): explicitly in the `skip` class today; stays there. We do NOT snapshot K8s-injected secrets.
- **Hostpath volumes** (sources outside the pod): out of scope. If the source pod mounted `/var/log/host` as hostPath, we snapshot the contents into the placeholder's hostPath (which usually points at the same host dir on the same node — works by accident, not by design). Cross-node hostPath replay needs separate design.
- **Quota / disk-space pre-check**: snapshots are bounded by source mount size. For pathological cases (multi-GB `/tmp` JIT caches) we may want a size cap and warn rather than fail. See open question.

## Testing plan

1. **vllm-8b TP=2 e2e** (`scripts/test-agent-driven-e2e.sh vllm-8b` with TP=2 in the yaml): the original motivating bug; should pass once `/dev/shm/psm_*` files travel.
2. **Regression: 5/5 single-GPU e2e** still PASS (all five existing scripts). Mount replay should be a no-op for any workload that doesn't need it; specifically, snapshots of empty `/dev/shm` should be tiny and the restore step should be fast.
3. **Unit tests** for `classifyMount` table-driven against curated mountinfo lines covering: K8s defaults (`/dev/shm` tmpfs rw), CDI binds, identity files, `/nvsnap-lib`, custom volumeMounts.
4. **Unit test** for `tarMount`+`untarIntoMntns` round-trip preserving xattrs/sparse/symlinks via a tmpdir fixture.
5. **Manual TP=2 inspection**: after restore, `kubectl exec` into placeholder, verify `/dev/shm/psm_*` files exist with same sizes as the source dump.

## Open questions for review

**All 6 questions resolved 2026-05-02. Ready for implementation.**


1. ~~**Size cap on snapshots**~~: **DEFERRED 2026-05-02** — the allowlist approach makes accidental large snapshots impossible by default (only `/dev/shm` ships in v1, and that's bounded by NCCL segment sizes — typically <1 GB). Revisit if a future allowlist entry warrants per-mount size caps.

2. ~~**emptyDir vs tmpfs handling**~~: **DEFERRED 2026-05-02** — moot under the allowlist approach. We snapshot whatever the allowlist names, regardless of fstype.

3. ~~**Snapshot timing relative to cuda-checkpoint Lock**~~: **DECIDED 2026-05-02 — snapshot AFTER cuda Lock, before CRIU dump.** No GPU process can write into the snapshot path while we're capturing it; safety wins over a marginal time saving from snapshotting earlier.

4. ~~**Compression**~~: **DECIDED 2026-05-02 — uncompressed for now.** Initial release prioritizes speed; compression adds CPU on a hot path. Revisit only if snapshot size becomes a real bottleneck in production.

5. ~~**Tarball layout**~~: **DECIDED 2026-05-02 — `<checkpointDir>/mounts/<sanitized-path>.tar`** (e.g. `<ckpt>/mounts/dev_shm.tar`). Sanitization: `/` → `_`. Keeps replay artifacts in their own subdir, doesn't pollute the checkpoint root alongside CRIU's `.img` files, leaves room for sibling files later (e.g. `mounts/dev_shm.json` manifest).

6. ~~**Failure semantics**~~: **DECIDED 2026-05-02 — warn-and-continue for replay mounts; fatal for rootfs.** Replay mount failures get logged but don't abort the operation — CRIU may still succeed if the workload didn't depend on the missing content. Worth trying optimistically; if restore then fails on a missing file, we have the warning in the log to point at. Rootfs mirror remains fatal-on-error (the rest of the restore is meaningless without it).

## Implementation order (after design approval)

1. `classifyMount` + tests (`internal/agent/checkpoint_plan_a.go`).
2. `tarMount` + `untarIntoMntns` + tests (`internal/agent/rootfs_diff.go`).
3. `ReplayMount` metadata field (`pkg/metadata/...`).
4. Wire snapshot pass into checkpoint flow.
5. Wire replay pass into restore flow.
6. e2e tests: TP=2, all 5 single-GPU regression.
7. PR.

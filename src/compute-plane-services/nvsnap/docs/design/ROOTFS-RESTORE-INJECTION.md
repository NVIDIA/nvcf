# Design: Capture-Type-Aware Restore Injection (v0.0.55)

Status: implementing
Owner: Balaji Ganesan

## Problem

The restore webhook (`internal/webhook/mutate.go` `Mutate`) routes a pod
with `nvsnap.io/restore-from=<hash>` to one of two injectors:

1. **`tryL2Mount` + `restoreBundleInjectPatches`** (`l2_mount.go`,
   `restore_entrypoint.go`) — when a Bound `rox-<hash>` PVC exists.
   Mounts the rox PVC at `/nvsnap-checkpoint`, sets `CHECKPOINT_PATH`,
   and **overrides the container command to `/nvsnap/restore-entrypoint`**
   (go-criu). It assumes the L2 PVC contains a **CRIU dump**.

2. **`buildPatches`** (the overlay path) — otherwise. Mounts the
   captured tree from the L1 hostPath cache, overlays
   `RootfsExtractPaths` (engine cache/model dirs) onto the container,
   pins to the captured node via nodeAffinity, and **keeps the
   container's original command** → warm normal start. This is what
   `scripts/test-e2e.sh` rootfs restore exercises, and it works.

The dispatch branches on **"does a Bound rox PVC exist"**, NOT on the
**capture type**. So a *rootfs* capture promoted to an L2 rox PVC
(the rootfs-everywhere + L2 fan-out path) hits branch 1 → gets the CRIU
`restore-entrypoint` → which waits forever for
`/nvsnap-checkpoint/inventory.img` that a rootfs capture never produced.

Repro (GCP-H100-a 2026-06-10): NVCA whisper restore — rox PVC bound,
pod Running, but the inference container hung in
`=== NVSNAP Restore Entrypoint (go-criu) === Waiting for checkpoint at
/nvsnap-checkpoint/inventory.img …`. The rox PVC holds `rootfs/` +
`volumes/`, not CRIU images.

## How the scripts avoided it

Script rootfs restore never created an L2 rox PVC — it restored from
the L1 hostPath cache, so `Mutate` always fell through to `buildPatches`
(the overlay path). The CRIU L2 fast-path was added for the CRIU-on-L2
case and silently swallowed rootfs-on-L2.

## Design

Branch on **capture type**, resolved from the manifest, not on PVC
existence.

### Capture-type signal

`Backend.Stat(hash)` returns the `Manifest`. A **rootfs** capture has
`Volumes[].Type == "rootfs"` and/or non-empty `RootfsExtractPaths`. A
**CRIU** capture has neither (its tree is CRIU image files). Add a
helper `manifestIsRootfs(m)`:

```go
func manifestIsRootfs(m checkpointstore.Manifest) bool {
    if len(m.RootfsExtractPaths) > 0 {
        return true
    }
    for _, v := range m.Volumes {
        if v.Type == "rootfs" {
            return true
        }
    }
    return false
}
```

### New dispatch in Mutate

```
resolve hash
manifest = Backend.Stat(hash)            // Stat FIRST (currently after L2)
if manifestIsRootfs(manifest):
    → buildPatches(...)                  // overlay path, original command
      (sources the lower layer from the rox PVC when L2 is bound,
       else L1 hostPath — see below)
else:                                     // CRIU capture
    if L2Backend bound: tryL2Mount + restore-entrypoint   // unchanged
    else: existing CRIU L1 restore-bundle path            // unchanged
```

CRIU behavior is byte-for-byte unchanged. Only rootfs captures change:
they now always take the overlay path, never `restore-entrypoint`.

### Rootfs overlay sourced from the L2 rox PVC

`buildPatches` emits, per captured volume, a mount via
`Backend.Mount(hash, vm)`. Today that's the L1 hostPath backend. For a
rootfs capture with a Bound rox PVC we want the lower layer to come from
the **rox PVC** (so cross-node fan-out is preserved — rox is ReadOnlyMany,
multi-attach) instead of pinning to the captured node's hostPath.

The rox PVC holds the whole tree; the bytes for an extract path
`/opt/nim/.cache` live at `rootfs/opt/nim/.cache`, and user-data volume
`model-data` lives at `volumes/model-data`. So each injected mount is the
**same rox PVC with a `subPath`**:

| Captured item | rox PVC subPath | container mountPath | mode |
|---|---|---|---|
| ExtractPath `/p` | `rootfs/p` (strip leading `/`) | `/p` | RO |
| user-data volume `N` at `/m` | `volumes/N` | `/m` | RO |

v1 mounts these **read-only**. NIM/Riva/Triton inference caches and
model repos are read-only at serve time, so RO subpath mounts give a
warm start with zero copy and full fan-out. (A per-pod writable
OverlayFS upper over the rox lower — for engines that *write* to their
cache dir at runtime, e.g. first-touch `torch.compile`/Triton — is a
fast-follow; the existing `OverlayPreparer` already does this for L1
hostPath lowers and can be extended to a PVC-subpath lower. Not needed
for whisper/Riva.)

Mechanism: extend `PerCapturePVCBackend.Mount` to honor a `SubPath`
on `VolumeMeta` and emit `readOnly` subpath volumeMounts of the rox
PVC. `buildPatches` (rootfs + L2 bound) iterates `RootfsExtractPaths`
+ user-data `Volumes` and calls `L2Backend.Mount` with the subPath set.
The rox PVC `Volume` is added once (dedup by name); one `volumeMount`
with `subPath` per item. No command override; original entrypoint runs.

### Why not keep using restore-entrypoint for rootfs

`restore-entrypoint` is a CRIU process-restore driver — it execs
`criu restore` against image files. A rootfs capture has no process
image; restore is "make the warmed files visible, start normally." The
two are fundamentally different restore modes and must not share the
injector.

## Files touched

| File | Change |
|---|---|
| `internal/webhook/mutate.go` | `Mutate`: Stat manifest before L2 branch; dispatch on `manifestIsRootfs`. Rootfs → `buildPatches` (L2-aware). Add `manifestIsRootfs`. |
| `internal/webhook/l2_mount.go` | `tryL2Mount` now only used for CRIU captures (unchanged behavior; caller-gated). |
| `internal/webhook/rootfs_l2_mount.go` (new) | Build RO subpath mounts of the rox PVC for a rootfs capture's extract paths + user-data volumes. |
| `internal/checkpointstore/percapture_pvc.go` | `Mount` honors `VolumeMeta.SubPath` → emits a `subPath` RO volumeMount of the rox PVC. |
| `internal/checkpointstore/store.go` | `VolumeMeta.SubPath string` field (the path within the captured tree). |
| `internal/webhook/*_test.go` | rootfs-on-L2 → subpath mounts + NO command override; CRIU-on-L2 → restore-entrypoint unchanged. |

## Acceptance

1. NVCA whisper restore: pod gets RO rox subpath mounts at the engine
   cache/model paths, **original command preserved**, no
   `restore-entrypoint`, reaches `/health` 200 warm — no re-capture, no
   `inventory.img` wait.
2. CRIU restore (vllm-small single-GPU) unchanged: still
   `restore-entrypoint` + `CHECKPOINT_PATH`.
3. Cross-node fan-out preserved: a second restored pod on a different
   node mounts the same rox PVC (ROX multi-attach) and serves.
4. Webhook unit tests green for both branches.

## Out of scope

- Writable OverlayFS upper over the rox PVC lower (fast-follow for
  engines that write to cache at runtime).
- L1→L2 source unification in `buildPatches` for non-NVCA script flows
  (scripts keep using L1 hostPath; unchanged).
- 96Gi PVC over-sizing (separate fix: size rwx/rox from
  `manifest.TotalSizeBytes × margin`, not vRAM — tracked separately).

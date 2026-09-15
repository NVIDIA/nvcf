# Design: Bounded-size rootfs restore (keep mount topology out of the Pod object)

Status: implemented
Date: 2026-06-11
Related: #194 (overlayfs-at-restore), #199 (whole-rootfs-restore), #189 (Hook B capture-once), rule 20 (capture-path matrix)

## Problem

A rootfs warm-restore of a fat multi-library image fails at admission:

```
failed to create instance ... err: etcdserver: request is too large
```

Observed on GCP-H100-a, gpt-oss-120b (vllm TP=4): capture recorded **1055 extract
paths** (857 `rootfs-extract` + 197 vllm `__pycache__` + 1 hf-cache). The restore
Pod never gets created → NVCA ICMS request stuck `RequestPending` → no restore.

Root cause: the webhook encodes the **per-path overlay topology into the Pod
object**, and every Pod is persisted to etcd, which rejects objects over
`--max-request-bytes` (~1.5 MiB default). Nothing about the 61 GB of *data* is in
etcd — only the *mount list* — but at ~1000 paths the mount list alone blows the cap.

Single-GPU whisper (few captured paths) stays under the cap; that is the only
reason it worked. This is a **path-count ceiling, not a storage-medium issue** —
NVMe (L1) and rox-PVC (L2) both inject per-path today, so both hit the same wall
at high path count. EROFS is out of scope (too slow for restore).

## Current implementation (what bloats the Pod)

- `internal/rootfsonly/enumerate.go` emits one mount point per captured leaf
  subtree (granularity: shallowest-dir-with-files, `mountPointMinBytes`=100 KB,
  HF-hub/Triton signatures, `neverMountWholeDirs` guard for base-owned FHS dirs).
- L2 path `internal/webhook/rootfs_l2_overlay.go`: 3 fixed volumes (rox/scratch/
  merged), but **one `volumeMount` (subPath) on the main container per captured
  path**, plus an init-container script with **one `mount -t overlay` line per
  path**. N paths → O(N) Pod-spec entries + O(N) script bytes.
- L1 path `internal/webhook/mutate.go` `buildPatches`: injects one `volumeMount`
  per captured Volume / RootfsExtractPath (same O(N) shape).

So the size is dominated by N main-container `volumeMounts` + the N-line init
script. Long captured paths (`/usr/local/lib/python3.12/dist-packages/vllm/...`)
appear ~3× each (mountPath, subPath, mount cmd), so ~1000 paths ≈ ~1 MiB before
the rest of the Pod (env, args, NVCA injections) pushes it over.

## Why per-path was chosen (the constraint we must not regress)

Earlier whole-dir mounts **masked image files**: capturing a partial subtree (e.g.
only the compiled `.pyc` under a package) and overlay-mounting that captured dir
as the *sole* lowerdir hides the image's `.py` source at the same path
(v0.0.62 regression → v0.0.63 `neverMountWholeDirs` guard). Per-path leaf mounts
avoided masking by overlaying *only* the exact captured leaves and letting the
image show through everywhere else.

So any fix must preserve: **captured changes appear, image files are NOT masked,
customer mounts win, base-owned system dirs aren't clobbered.**

## Principle (hard invariant)

**The Pod object carries O(1) nvsnap mounts, independent of capture path count.** No
per-path `volumeMounts`, no per-path init-script lines, no bounded-K coarse list —
a 5-path capture and a 50,000-path capture produce the same Pod size. The full
warm rootfs is materialized **node-side**; the Pod never carries the topology.

Foundation for both providers: the captured tree is the container's overlay
**upperdir** (files changed on top of the image), so the exact warm rootfs is
`overlay(lowerdir=<image>, upperdir=<captured>, workdir=<scratch>)`. Captured-over-
image means **nothing is masked** (image files show through, captured win) — the
whole tree is correct by construction, and the `neverMountWholeDirs` special-casing
that per-path needed disappears.

## The tradeoff triangle

No single mechanism gives all three. Pick the provider per environment:

| | runtime-agnostic | zero-cap / zero-mount | zero-data-movement |
|---|---|---|---|
| **B′** in-container overlay + pivot_root | ✅ | ✗ (CAP_SYS_ADMIN) | ✅ |
| **C** containerd proxy snapshotter | ✗ (containerd) | ✅ | ✅ |
| (rejected) OCI image layer | ✅ | ✅ | ✗ (re-ships 61 GB) |
| (rejected) coarse bounded-K | ✅ | partial | ✅ — but a *limit*, vetoed |

## Abstraction: `RootfsProvider`

Materialization is pluggable, selected by detected runtime at agent startup, so we
are **not locked** to one runtime:

```
type RootfsProvider interface {
    // Materialize makes the warm rootfs (overlay of capture-over-image)
    // available for podUID on node, returning what the webhook must inject.
    Materialize(ctx, podUID, captureHash, image string) (Injection, error)
    Teardown(ctx, podUID string) error
}
// Injection is O(1): a single volume+mount (B′) or nothing at all (C).
```

- **containerd detected** → provider **C** (zero-cap, zero-mount).
- **anything else** → provider **B′** (runtime-agnostic fallback).

Same capture format (rox PVC for fan-out / NVMe host overlay for single-node)
feeds both.

## Provider B′ — in-container whole-rootfs overlay + pivot_root (runtime-agnostic)

Flow (mutating webhook + privileged init; no container-runtime API):

1. **Init container** `nvsnap-overlay-mount` (privileged; image = workload image so
   its own `/` is the image rootfs): mounts
   `overlay(lowerdir=<image rootfs>, upperdir=<captured>, workdir=<scratch>)` once
   into a shared `nvsnap-overlay-root` emptyDir (`mountPropagation: Bidirectional`).
   Upper is a per-pod writable scratch seeded from rox (fan-out) / NVMe
   (single-node) so engine startup writes (e.g. `/opt/nim/workspace`) land RW.
2. **Workload container** mounts `nvsnap-overlay-root` **once** (`HostToContainer`)
   and runs an injected shim that:
   - `mount --rbind` the API filesystems the kubelet set up under the *old* root —
     `/proc`, `/sys`, `/dev` (incl. the device-plugin-assigned `/dev/nvidia*`),
     `/dev/shm` — into the merged root,
   - `pivot_root` into the merged root, then `exec` the original `command`/`args`
     (preserved as env, as the CRIU path already does).

Cost: the shim needs **CAP_SYS_ADMIN** (for rbind + pivot_root). Crucially this is
**not `privileged`** — `SYS_ADMIN` grants mount power but does **not** alter the
device cgroup or add host device nodes, so the device plugin's single-GPU
isolation (the v0.0.66 fix) holds. Pod carries exactly one volume+mount.

## Provider C — containerd proxy snapshotter (zero-cap, zero-mount; containerd only)

A nvsnap **proxy snapshotter** (nydus/stargz/SOCI-style), registered in containerd
and fed by the agent DaemonSet:

1. At capture-promote the agent registers the captured tree (rox/NVMe) as a
   snapshot keyed by capture hash.
2. On restore, the function Pod is admitted with an annotation/label naming the
   capture hash; the container's rootfs snapshot is **prepared from the nvsnap
   snapshotter** as an upper layer over the image's chain — so the kubelet/runtime
   builds the warm rootfs **natively**.
3. `/proc`, `/dev`, `/dev/nvidia*`, `/sys` are set up by the runtime as for any
   container — **no shim, no rbind, no extra cap, zero Pod mounts.**

Cost: real infra — a snapshotter binary + per-node containerd config rolled out by
the DaemonSet; containerd-specific. This is the clean production path on our actual
runtime (GKE/containerd, what NVCF runs on).

## Recommendation & sequencing

Ship the `RootfsProvider` interface with **B′ first** (unblocks gpt-oss /
multi-GPU now, runtime-agnostic, costs SYS_ADMIN), then land **C** as the
containerd-optimized provider (drops the cap + shim where available). No flag-day:
selection is by detected runtime; B′ remains the universal fallback.

## Production / scale considerations (both providers)

- **etcd object size**: hard invariant — Pod mounts O(1). Unit test: a manifest
  with N=5000 paths injects the **same** Pod size as N=5.
- **Fan-out**: rox PVC (ReadOnlyMany, zero-copy, multi-node) is the capture source
  for fan-out; NVMe host overlay is the single-node fast path. Both providers
  consume the same source. Per-pod writable upper stays per-pod.
- **GPU isolation**: preserved in both — B′ adds only `SYS_ADMIN` (not privileged);
  C adds nothing. Never set `privileged` on the workload (breaks isolation,
  v0.0.66).
- **Customer-conflict**: if the customer's Pod already mounts a path, B′'s
  whole-root overlay still honors it (their mount is layered last); C is below the
  Pod's volumeMounts entirely. No path-shadowing logic needed.
- **Idempotency / churn**: orthogonal (see #189 Hook B capture-once + catalog
  dedup + retention-based PVC GC).

## Phased plan

1. **Interface**: define `RootfsProvider` + runtime detection + webhook wiring that
   asks the provider for the O(1) `Injection`. Replace the per-path injection in
   `rootfs_l2_overlay.go` + `mutate.go` buildPatches.
2. **B′ provider**: init overlay assembly (capture-over-image) + the rbind +
   pivot_root shim + exec original command. Add `CAP_SYS_ADMIN` (not privileged) to
   the workload; keep v0.0.67 file caps.
3. **Unit test**: constant Pod size for N=5 vs N=5000 paths (one volume/mount).
4. **e2e (B′)**: gpt-oss-120b TP=4 warm restore on GCP-H100-a (the failing case) +
   whisper single-GPU regression + a vllm single-GPU + 16-pod fan-out re-validate.
5. **C provider**: nvsnap proxy snapshotter + DaemonSet containerd config; agent
   registers snapshot at promote; provider returns empty `Injection`. e2e same
   matrix; confirm zero Pod mounts + no `SYS_ADMIN`.
6. **Bench**: restore latency vs validated whisper ~51s for both providers.

## Rollback

Per-provider. The interface keeps the per-path injector available as an emergency
provider for low-path-count images. No capture data-format change — existing
captures (whisper, gpt-oss) restore under any provider.

## B′ entrypoint discovery (resolved direction)

B′ overrides the workload command with the shim, so the shim must know the
**original** entrypoint to exec post-pivot_root. ENTRYPOINT-only images (NIM,
whisper) carry no `command` in the Pod spec, so the webhook can't read it there.
Resolution (runtime-agnostic, no image-config fetch): the **agent records the
source container's PID-1 `argv` + cwd at capture time** (it already inspects the
source process) into the manifest; the webhook sets `NVSNAP_ORIG_COMMAND`/`ARGS`
from the manifest; the shim execs it. Provider **C avoids this entirely** (runtime
runs the image's normal entrypoint — no shim, no command discovery), a further
point for C where containerd is available.

Status: `cmd/nvsnap-rootfs-restore` (the B′ shim) is implemented and compiles.
Remaining B′ wiring: capture-side argv recording → manifest field → webhook O(1)
injection (rox mounted once at `/nvsnap-captured` + scratch emptyDir + `/nvsnap`
bundle + command=shim + `CAP_SYS_ADMIN`) replacing the per-path injector.

## Open questions

- **B′**: confirm rbind set is complete for vllm/NIM (`/proc`, `/sys`, `/dev`,
  `/dev/shm`, cgroup?); pivot_root vs `MS_MOVE`+chroot ergonomics under the shim.
- **C**: proxy snapshotter base (nydus vs stargz vs SOCI vs bespoke); how the Pod
  names the snapshot to containerd (image-ref convention vs label the CRI plumbs
  through); per-node config rollout + upgrade story via the DaemonSet.
- **NVCA**: both providers change/relax the workload securityContext or command —
  confirm composition with NVCA's own mutations (B′ command shim; C annotation).

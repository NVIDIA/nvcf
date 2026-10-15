# L2 per-capture PVC for the CRIU path (nvsnap#63)

>  **Status:** The per-capture PVC fan-out described here is current and
>  shipping. This document is framed around the CRIU dump path; the same L2
>  mechanism now also serves the rootfs/cachedir capture paths (the primary
>  paths today). See [ROOTFS-EVERYWHERE](design/ROOTFS-EVERYWHERE.md) and
>  [STORAGE-AGNOSTIC-L2-PROMOTION](design/STORAGE-AGNOSTIC-L2-PROMOTION.md).

**Issue**: nvsnap#63 · **Last updated**: 2026-05-31

## Goal

Make multi-node restore scale. Today the CRIU restore path either fetches
the dump over HTTP from the capture node (peer cascade) or pulls it from
the global blobstore. Both bottleneck on the source node's egress, so
the 200-pod fan-out target collapses to a serial pull.

The fix is to land the dump in a per-capture PVC on production-grade
shared storage and have every restored pod mount it ReadOnly. Pod
spin-up reads at storage-tier speed in parallel across all nodes.

> **Storage class requirement.** The writer PVC (`rwx-<hash>`) is
> mounted **ReadWriteOnce** by a single writer Job pod
> (lease-serialized one-per-hash). After the writer completes the
> backend takes a VolumeSnapshot, clones it to `rox-<hash>` with
> access mode **ReadOnlyMany**, and restored pods mount the rox
> PVC read-only. Any standard CSI works — including GKE Hyperdisk
> ML (`pd.csi.storage.gke.io`), which supports exactly the RWO/ROX
> pair we use.
>
> An earlier draft of this design used `ReadWriteMany` for both
> PVCs, which excluded every block-storage CSI; that requirement
> was relaxed in nvsnap#81 once we noticed the writer is single-pod
> and readers never mount RW. The `rwx-` prefix on the writer
> PVC's name is historical — kept for backward compat / log
> grepability.

The PVC name reflects its lifecycle state: `rwx-<hash>` during write
(writer Job mounts RW, populates), then `rox-<hash>` once frozen
(restored pods mount RO). The transition is a VolumeSnapshot +
clone: `rwx-<hash>` is snapshotted, `rox-<hash>` is created from
the snapshot, `rwx-<hash>` is deleted. Snapshots are cheap on
Hyperdisk ML (copy-on-write), and the two-name convention makes the
state unambiguous in `kubectl get pvc` — no PVC named `rox-` is
ever mounted RW.

This proposal is **CRIU-path only**; the rootfs/EROFS path already has
its own per-capture PVC orchestrator (`internal/rootfsonly`).

## Non-goals

- Re-introducing the PV-flip pattern. The old `gpdrox` package did
  RW-then-flip-to-RO; we don't need that. Provision RWX from the start,
  mount RO on restored pods.
- Replacing L1 hostpath or L4 blobstore. PVC sits between them as L2.
- Cross-cluster replication. PVCs are cluster-scoped; cross-cluster
  goes through L4 blobstore (covered by the transport doc).

## Context: what's there today

| Layer | Tier | Status |
|---|---|---|
| L1 | per-node hostpath (`/var/lib/nvsnap/checkpoints/<hash>__<ts>`) | working — capture writes here |
| L2 | per-capture RWX PVC (`rox-<hash>`) | **missing for CRIU**; works for rootfs |
| L3 | peer HTTP cascade | working — restore-side `cascade_fetch.go` |
| L4 | nvsnap-blobstore (CAS) | working — `UploadCheckpoint`, durable |

The infra for L2 mostly exists:

- `internal/checkpointstore.Backend` interface (Put/Get/Stat/Delete + Mounter)
- `internal/checkpointstore.CaptureSource` discriminator (currently
  only `SourceKindRootfs` defined)
- `agent.captureBackend` field, populated when `--rootfs-capture` is
  on. Comment explicitly anticipates CRIU promotion to the same backend.
- `cmd/agent capture-write` subcommand: one-shot Job that materializes
  a CaptureSource into the backend's PVC.
- Restore-entrypoint's `CHECKPOINT_PATH` env var: path-agnostic, takes
  any directory containing the CRIU images.

What's missing for CRIU:

1. A Backend impl that creates `rwx-<hash>` (RWX, RW writer) per
   capture, snapshots it on completion, and lands the durable
   artifact as `rox-<hash>` (RWX storage, ReadOnly mounts only).
2. `SourceKindCRIU` in the CaptureSource enum.
3. The CRIU-path `promoteCheckpointToBackend` call site (removed in
   commit `5ea039c`).
4. Catalog field for `pvc_name` so restore-side can find the PVC.
5. Mutating-webhook + NVCA Hook A stamping the PVC mount + env var
   on restored pods instead of the hostPath volume.

## Why the old gpdrox failed (and what's different now)

The deleted package used PV-flip: writer Job mounts RWX, after copy
the volume is flipped to RO via patch, restored pods mount RO. CRIU
restore failed because **restore-entrypoint was writing
`restore-criu.conf` into the checkpoint directory** — which after the
flip was read-only.

The fix landed in restore-entrypoint as of `cmd/restore-entrypoint/main.go:979`:
all CRIU runtime files go to `/tmp` (`restore-criu.conf`, `restore.pid`,
`criu-lazy-pages.log`, `nvsnap-output.log`, core dump pattern). The
checkpoint directory is purely read input.

With that constraint dropped, we can:

- Skip the flip. Provision RWX, mount RO directly on restored pods.
  Simpler, no two-state object to reason about, no race window.
- Use any RWX StorageClass (Hyperdisk ML, GCP Filestore, AWS EFS,
  Azure Files, NFS). All support concurrent RO mount.

## Architecture

```
                                                                  restore pods (any node)
                                                                        │
                                                                        ▼
   capture node                              rwx-<hash>  ──snapshot──►  rox-<hash>
       │                                     (writer Job mounts RW,    (RWX storage,
       │  CRIU dump                          writes, then deleted)     mounted RO by
       ▼                                                                every restore pod)
   /var/lib/nvsnap/checkpoints/<hash>__<ts>  ──►  capture-write Job
   (L1, hostpath, source of truth)             reads L1 → writes rwx-<hash>
                                                takes snapshot, creates rox-<hash>,
                                                deletes rwx-<hash>, registers in catalog
```

Flow on capture:

1. Agent finishes CRIU dump (`internal/agent/checkpoint.go`).
2. Agent registers in catalog with `hash` (nvsnap#61, just landed).
3. **(new)** Agent calls `captureBackend.Put(ctx, hash, []CaptureSource{{Kind: SourceKindCRIU, SrcPath: hostDumpDir, DstSubpath: ""}}, manifest)`.
4. Backend provisions `PersistentVolumeClaim/rwx-<hash>` with
   `accessModes: [ReadWriteMany]` + the configured StorageClass.
5. Backend spawns the capture-write Job (existing pattern, just
   handle `SourceKindCRIU`): rsync the L1 directory into the PVC mount,
   write `manifest.json` at PVC root, exit.
6. Backend takes a `VolumeSnapshot` of `rwx-<hash>`.
7. Backend creates `rox-<hash>` from the snapshot. The new PVC keeps
   the RWX access mode at the storage layer (so multiple readers can
   mount it concurrently), but its name + intent says "read-only" —
   the webhook mutator only ever stamps it with `readOnly: true` and
   no Job ever mounts it RW. The two-name convention makes the
   lifecycle visible: a `rox-` PVC is never RW-mounted, ever.
8. Backend deletes `rwx-<hash>` + its snapshot (snapshot served its
   purpose; `rox-<hash>` is the durable artifact).
9. Backend updates catalog row with `pvc_name = rox-<hash>`.
10. Async L4 upload to blobstore continues in parallel (unchanged).

Flow on restore:

1. Hook A / mutating webhook resolves `restore-from: <hash>` for the
   new pod.
2. Webhook queries catalog `/lookup?hash=<hash>` → gets `pvc_name`,
   `peers[]`, `blob_uri`.
3. **(new)** If `pvc_name` is set, webhook mutates the pod spec:
   - adds `volumes: [{name: nvsnap-checkpoint, persistentVolumeClaim: {claimName: <pvc_name>, readOnly: true}}]`
   - adds `volumeMounts: [{name: nvsnap-checkpoint, mountPath: /nvsnap-checkpoint, readOnly: true}]` to restore-entrypoint container
   - sets `env: [{name: CHECKPOINT_PATH, value: /nvsnap-checkpoint}]`
   - **omits** the hostPath volume and the nodeAffinity-to-capture-node selector
4. If `pvc_name` is empty (older capture, or PVC was GC'd), the webhook
   falls back to the existing hostPath + nodeAffinity path.
5. Restore-entrypoint runs unchanged — opens `$CHECKPOINT_PATH`, doesn't
   care whether it's a hostPath or a PVC.

## Component changes

### 1. CaptureSource

```go
// internal/checkpointstore/store.go
const (
    SourceKindRootfs SourceKind = "rootfs"
    SourceKindCRIU   SourceKind = "criu" // new
)
```

`SourceKindCRIU` materialization: backend treats `SrcPath` as a CRIU
dump directory, copies the whole tree (with no excludes — CRIU images
have no junk) under `DstSubpath` (default empty = PVC root).

### 2. Backend impl

New type in `internal/checkpointstore/`:

```go
type PerCapturePVCBackend struct {
    client       kubernetes.Interface
    namespace    string      // where to create the PVCs (nvsnap-system)
    storageClass string      // e.g. "hyperdisk-ml", "filestore-rwx"
    sizePadding  float64     // e.g. 1.2 = request 1.2x the dump size
    writerImage  string      // capture-write image
    log          logrus.FieldLogger
}

var _ checkpointstore.Backend = (*PerCapturePVCBackend)(nil)
```

- `Put`: create PVC, spawn writer Job, wait for completion, record
  `pvc_name` in the returned manifest.
- `Get`: NOT used on restore side — restored pods mount the PVC
  directly via the webhook. `Get` exists for backend-to-backend copies
  (e.g., re-hydrate from blobstore) and would spawn a reader Job. Out
  of scope for nvsnap#63; can stub or no-op for now.
- `Stat`: check whether `PVC/rox-<hash>` exists in the namespace.
- `Delete`: delete the PVC. Retention (#42) calls this.

### 3. Catalog schema

Add `pvc_name TEXT` column to the `checkpoints` table. Backwards-
compatible: missing on old rows = "no PVC, use existing fallback".

```sql
ALTER TABLE checkpoints ADD COLUMN pvc_name TEXT DEFAULT '';
```

Surfaced via `GET /api/v1/checkpoints/{id}` and the `lookup?hash=` endpoint.

### 4. CRIU capture-path promote

Re-add a slim version of `promoteCheckpointToBackend` in
`internal/agent/checkpoint.go`, gated on a config flag:

```go
if a.captureBackend != nil && a.config.PromoteCRIUToBackend {
    promoteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
    defer cancel()
    src := checkpointstore.CaptureSource{
        Kind:       checkpointstore.SourceKindCRIU,
        SrcPath:    a.checkpointHostPath(checkpointDir),
        DstSubpath: "",
    }
    manifest, err := a.captureBackend.Put(promoteCtx, catalog.Hash, []checkpointstore.CaptureSource{src}, baseManifest)
    if err != nil {
        log.WithError(err).Warn("L2 promote failed; restore will fall back to peer/blob")
    } else {
        // PATCH catalog row with manifest.PVCName so the webhook can find it.
        a.updateCatalogPVC(ctx, checkpointID, manifest.PVCName)
    }
}
```

Promote is synchronous: webhook needs the PVC to exist before NVCA
marks Warm, otherwise the first restored pod races against creation.
For 80 GB dumps on Hyperdisk ML the writer Job is bounded by storage
write throughput — expect 30s–2min for vllm-llama-8b-FP8.

### 5. Webhook + NVCA Hook A

The mutating-webhook side (in `internal/webhook/`) currently injects
hostPath volume + nodeAffinity. Extend the mutation:

- Look up the catalog row for the stamped hash.
- If `pvc_name` is non-empty: inject PVC volume + RO mount, drop nodeAffinity.
- Otherwise: existing hostPath path.

Same change in NVCA Hook A (`pkg/nvca/nvsnap_hook.go`) — it does the
same kind of mutation on its own admission path.

### 6. Restore-entrypoint

**No code change.** `CHECKPOINT_PATH` env var already drives the
resolver. The PVC mount supplies the directory; the env var supplies
the path. Path-agnostic.

## StorageClass selection

A working RWX StorageClass is a **cluster prerequisite for nvsnap** —
not a feature toggle. Customer-installation docs will call this out
the same way they call out the NVIDIA driver minimum.

The Backend takes `storageClass` as helm value
`agent.l2.storageClass`. Resolution order:

1. Explicit operator override (the helm value).
2. First StorageClass in the cluster with `Provisioner=*hyperdisk-ml*`
   or `*filestore*` or `*efs*` (RWX-capable production tiers).
3. **If none match: agent refuses to start.** Fatal log calls out
   the missing prerequisite + links to the install doc. Better to
   surface a missing prereq at deploy time than serve degraded
   captures silently.

Detection runs at agent startup. Result lands in `/api/v1/health` so
operators can confirm which StorageClass is in use.

## PVC lifecycle

- **Provisioning**: synchronous in `Put`. Wait up to `30 minutes` for
  the writer Job to complete. Failure modes: PVC quota exhausted,
  StorageClass unhealthy, writer Job OOM. All emit a Warn + fall
  through to "L2 promote failed; restore will use peer/blob" — never
  fail the capture.
- **Retention**: tie into the existing retention controller (#42).
  When a checkpoint row is deleted, also delete its PVC. The cascade-
  delete fix from !20 already calls Backend.Delete; the new Backend
  impl just needs Delete to cleanly remove the PVC.
- **Orphan GC**: nightly sweep — list all `nvsnap-system` PVCs labeled
  `nvsnap.io/per-capture=true` and missing a corresponding catalog row.
  Delete them. Same pattern as the rootfs orchestrator's GC.

## Failure modes

| Failure | Behavior |
|---|---|
| StorageClass missing | L2 disabled at startup; INFO log; capture still works via L1+L4 |
| PVC provision fails (quota, etc.) | Capture succeeds (L1 + L4 unchanged); webhook falls back to hostPath restore |
| Writer Job fails | Same as above |
| PVC GC'd between capture and restore | Webhook's lookup-by-hash gets `pvc_name=""`; falls back to peer cascade |
| Multiple restored pods on same node | RWX PVC allows it; kubelet shares the mount |
| Old agent capture (no `pvc_name`) | Restore falls back to peer/blob via existing path |

## Test plan

Unit (in this MR or follow-up):

- `PerCapturePVCBackend.Put` with fake K8s client: verify PVC created
  with right StorageClass + access mode + size + labels.
- `Put` idempotency: second call for same hash returns existing PVC,
  doesn't spawn a duplicate Job.
- `Stat` returns `ErrNotFound` for missing hash, `Manifest` for present.
- `Delete` removes the PVC; idempotent on already-deleted.
- Webhook mutation: pod stamped with `restore-from` and a hash that has
  `pvc_name` ends up with PVC volume + RO mount + `CHECKPOINT_PATH`
  env, NO hostPath, NO nodeAffinity.
- Webhook mutation: same hash with empty `pvc_name` falls back to
  hostPath + nodeAffinity (existing behavior preserved).

E2E (live cluster):

- vllm-llama-3.1-8b-FP8 capture on GCP-H100-a:
  - verify `rox-<hash>` PVC exists post-capture
  - verify catalog row has `pvc_name`
  - scale function to 5 pods on different nodes
  - each pod's spec has PVC mount (not hostPath)
  - all 5 restore in parallel from the PVC (measure time vs baseline
    peer-cascade)
- Retention sweep: delete the catalog row, confirm PVC is also deleted.
- Orphan GC: pre-seed a labeled PVC with no row, confirm next sweep
  deletes it.

## Rollout

Phase 1 (this MR sequence):

- MR-A: Backend impl + `SourceKindCRIU` + catalog schema migration
- MR-B: agent capture-path promote (synchronous, gated by config flag,
  default off)
- MR-C: webhook + Hook A mutation updates
- MR-D: enable on GCP-H100-a (helm values set
  `agent.l2.enabled=true`, `agent.l2.storageClass=hyperdisk-ml`); e2e

Phase 2 (later):

- Auto-detect StorageClass at startup (vs requiring explicit value).
- Backend.Get implementation (re-hydrate from blobstore into a fresh PVC
  when L2 was GC'd but L4 still has it).
- Cross-region PVC replication (out of scope; transport doc).

## PVC size: GPU vRAM × 1.2

Provision `gpu_vram_bytes × gpu_count × 1.2`. Rationale:

- For CRIU GPU dumps the vRAM pages dominate. Observed on
  vllm-llama-3.1-8B-Instruct-FP8 (H100, 0.85 util):
  - 80 GB total dump
  - 71 GB vRAM pages
  - 8.6 GB rootfs-diff
  - 0.7 GB CPU pages
  - tens of MB CRIU bookkeeping
- The non-vRAM tail is ~10 GB. At 1.2× of an 80 GB H100, the budget
  is 96 GB — ~16 GB headroom over a full-vRAM dump. Comfortable for
  the typical case.
- vRAM is already in the catalog row (`gpu_type` string parses to
  size, e.g. `"NVIDIA-H100-80GB-HBM3"` → 80 GB; `gpu_count` is the
  multiplier).
- Deterministic at capture time, no "predict the dump size" guess.

Failure mode: workloads with a fat rootfs-diff (huge model-compile
caches > 16 GB) blow past 1.2× and the writer Job fails with
OOSpace. The capture row stays without `pvc_name`, restore falls
back to peer cascade. Logged + metric'd so an operator can spot a
workload class that needs more headroom and bump the multiplier
per-namespace / per-fvID later.

## Open questions
2. **Job-vs-direct-write**: writer Job is cleaner (separate failure
   domain, easier to time-limit). Agent-direct-write would skip the
   Job pod startup overhead (~10s on cold pull) but mixes concerns.
   Stick with writer Job per existing pattern.
3. **Concurrent restores during writer Job**: if a restored pod's
   webhook fires before the writer finishes, the PVC exists but is
   empty. Mitigation: the webhook checks the Job's `Succeeded`
   condition (or the manifest.json at PVC root) before stamping the
   PVC volume mount. If still in progress, fall back to peer cascade
   for the first restore; subsequent restores get the PVC.
4. **NVCA `lookupByHash` shape**: extend the response to include
   `pvc_name`, or have Hook A do a separate Get? Tightest coupling =
   include it in `lookup` response, single round trip.

## Sync is per content hash, in the Backend

The restore-side resolver (next section) handles N readers waiting on
one writer. The symmetric concern is multiple capture *requests* for
the same content — and the right key for that sync is the
**content hash**, not the pod or fvID:

- Different fvIDs can hash to the same content (same
  `image_digest + model_id + engine_flags + driver_major` → same
  `CatalogInfo.Hash`). Locking on fvID would miss this.
- Two pods of the same fvID can drift to slightly different hashes
  (CPU page bytes vary per process). Locking on fvID would
  over-synchronize these legitimately distinct dumps.
- A single `nvsnap` CLI or `kubectl-nvsnap` invocation can also post
  for a content we already have. Any hash-keyed gate must catch
  that path too.

Sync lives in `Backend.Put(ctx, hash, ...)` — the single chokepoint
where every capture path (NVCA Hook B, CLI, webhook) meets the L2
storage layer. The catalog row is both the lock state and the
durable record of the artifact:

```
  Backend.Put(hash, sources):
    lease, err := acquireHashLease(ctx, hash)         // K8s Lease named "nvsnap-promote-<short_hash>"
    if alreadyHeld:
      poll catalog row until pvc_promote_state in {ready, failed}
      return existing manifest    (no work done)

    defer release(lease)
    if catalog.pvc_promote_state == "ready":
      return existing manifest    (raced, lost; reuse winner's artifact)

    set pvc_promote_state = "writing"
    create rwx-<hash> PVC + spawn writer Job + wait
    set pvc_promote_state = "snapshotting"
    snapshot rwx-<hash> → create rox-<hash> → delete rwx-<hash>
    set pvc_promote_state = "ready" + catalog.pvc_name = "rox-<hash>"
```

K8s `coordination.k8s.io/v1.Lease` is the right primitive: cluster-
scoped, atomic acquire, holder identity, automatic expiry on holder
crash (configurable lease duration, e.g. 15 min for a writer-Job-
sized window). Hash-keyed lease name ensures fvID-A and fvID-B that
hash to the same content share the lease.

Result: only one writer Job per content hash, cluster-wide,
regardless of how many fvIDs / pods / CLIs trigger captures. The
NVCA-side wastefulness (multiple Hook B reconciles for the same
fvID posting to nvsnap-server) is bounded — even if N reconciles fire,
only one wins the lease and runs the CRIU+writer path; the rest
become readers that wait on the same `pvc_promote_state`.

CRIU dump deduplication is a separate question: multiple captures
of the same fvID can produce distinct hashes due to CPU page drift,
and each goes through its own Backend.Put with its own lease. That's
expected — they're legitimately distinct artifacts. The lease
prevents *racing* on the same hash, not *running* per-hash captures.

### What NVCA Hook B does (and doesn't do) today

Verified by reading `pkg/nvca/nvsnap/controller/controller.go` +
`pkg/nvca/nvsnap/reconciler/reconciler.go` on 2026-05-31:

- Workqueue keyed on **pod**. N pod-ready events for the same fvID
  → N parallel reconciles → N `POST /api/v1/checkpoints`.
- Reconcile checks `optedOut` + `podReady` + dwells, then posts
  unconditionally. No CFS state gate.
- CFS states are `Cold/Fetching/Warm/Failed`. No `Capturing`.

The hash-keyed Backend lease (above) makes the L2 layer
correct regardless. But for *cost* (N parallel CRIU dumps each
burning a GPU for 2-3 min), an additional NVCA-side coalesce on
fvID would still be worthwhile. Filing as a quality-of-life
sibling issue on `nvca-nvsnap`, not a blocker for nvsnap#63.

## Restore-side resolver: where to fetch from

Every restored pod needs to answer "where do I read the dump from?"
With L1/L2/L3/L4 all in play, the resolver runs in priority order
inside the nvsnap-init init container (so the main restore-entrypoint
container starts only once a source is locked in):

1. **L1 hostpath**: check `/var/lib/nvsnap/checkpoints/<hash>__*`. If
   present, mount via hostPath, set `CHECKPOINT_PATH`. Done. This is
   the same-node fast path — no I/O at all.

2. **L2 rox PVC**: check whether `PersistentVolumeClaim/rox-<hash>`
   exists and is `Bound`. If yes, mount RO, set `CHECKPOINT_PATH`,
   done.

3. **L2 in-flight (writer Job hasn't finished yet)**: query catalog
   for `pvc_promote_state` (see state machine below). If `writing`
   or `snapshotting`, poll-wait. If `ready` by now, jump back to
   step 2. If `failed`, fall through.

4. **L3 peer cascade**: HTTP-fetch from a peer agent that has L1
   (existing `internal/agent/cascade_fetch.go` path). Used when L2
   failed or is unavailable.

5. **L4 blobstore**: pull from nvsnap-blobstore. Last resort,
   highest-latency tier.

### pvc_promote_state state machine

Stored on the catalog row, surfaced via `GET /api/v1/checkpoints/{id}`
and `/lookup?hash=...`:

| State | Meaning | Restore action |
|---|---|---|
| `""` (empty) | L2 not attempted (older capture, missing infrastructure) | Skip step 2/3, go to peer cascade |
| `pending` | `Put` accepted, writer Job not yet started | Poll-wait |
| `writing` | Writer Job mounting `rwx-<hash>`, copying data | Poll-wait |
| `snapshotting` | Writer succeeded, snapshot + `rox-<hash>` clone in progress | Poll-wait |
| `ready` | `rox-<hash>` is `Bound` and ready to mount | Mount and proceed |
| `failed` | Writer or snapshot errored | Fall through to peer cascade |

Transitions are written by the Backend during `Put`. State is
read-only from the restore-side; we never observe writer races
because writes happen on a single capture node per hash and the
final `ready` transition is atomic with the catalog UPDATE.

### Polling protocol

The nvsnap-init init container polls `GET /api/v1/checkpoints/lookup?hash=<hash>`:

- Interval: 2s with jitter (avoid thundering-herd on bulk scale-out).
- Timeout: `2 × estimated_writer_duration` based on `gpu_vram_bytes`.
  For a 96 GB PVC on Hyperdisk ML's published throughput (~500 MB/s
  write), that's ~3 min writer + ~30 s snapshot. Round to 8 min
  default.
- On timeout: log ERROR, fall through to peer cascade (step 4). The
  capture isn't broken; we just couldn't wait long enough.
- Optional optimization: also fire a peer-cascade fetch in parallel
  with the poll-wait, take whichever returns first. Phase 2 work
  (not in MR-A).

This collapses the bulk-scale-out race to a single source-of-truth:
every restore pod polls the same catalog row, no per-pod re-triggering
of the writer, no thundering herd into peer cascade.

## L2 is mandatory infrastructure

L2 PVC is **the** multi-node distribution tier. It's not a feature
to opt into — it's a cluster prerequisite. No `agent.l2.enabled`
config exists. The NvSnap install doc lists "RWX-capable
StorageClass" alongside other prereqs (NVIDIA driver, cuda-checkpoint,
kernel version). Agent refuses to start without one.

Per-capture L2 failure (PVC quota, writer Job error, slow binding)
still falls back gracefully — capture row keeps `pvc_name = ""`,
restore uses peer cascade. Capture itself never fails because L2
hit a snag on a single capture; a class-of-workloads issue is
visible via a `nvsnap_l2_promote_failed_total` counter for the
operator to investigate.

## Decision pending operator review

- StorageClass on GCP-H100-a: confirm `hyperdisk-ml` is the right
  one. Already provisioned per task #130.

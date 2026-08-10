# Storage-agnostic L2 promotion (PVC provider abstraction)

Status: **proposal** — design for review, not yet implemented.
Owner: nvsnap L2 / per-capture-PVC.
Related: nvsnap#63 (L2 tier), nvsnap#81 (RWO writer + ROX reader), nvsnap#171
(L2 backend too GCP-specific — this is the design that closes it).

## Problem

`internal/checkpointstore/percapture_pvc.go` (`PerCapturePVCBackend.Put`) is the
L2 tier: it turns one captured CRIU/rootfs tree into a durable, restore-side PVC
that N restored pods mount read-only. It works **only on GCP Hyperdisk-ML** today
because the rwx→rox transition is hard-coded to the CSI `VolumeSnapshot` +
clone-from-snapshot flow:

```
lease → create rwx PVC (RWO) → mount-holder copy → wait detach
      → VolumeSnapshot(rwx) → clone rox PVC (ROX) from snapshot → delete rwx → ready
```

The bracketed snapshot+clone is the **only** storage-specific part. Everything
else (hash-keyed lease serialization, the single-copy write phase, the
`pvc_promote_state` machine, namespace scoping, GC) is storage-agnostic.

We need to support, with no copy where the storage allows it:

| Storage | rwx→rox mechanism | Fan-out model |
|---|---|---|
| GCP Hyperdisk-ML | CSI VolumeSnapshot → clone | one shared **ROX** PVC, N readers |
| AWS EBS (gp3/io2) | CSI VolumeSnapshot → clone | **RWO only**: one clone **per restore pod** |
| NVMesh | **none** — reuse the same volume | N static **ROX** PVs sharing a transformed `volumeHandle` (zero copy) |
| CephFS / Filestore / EFS (RWX filesystems) | none — mount in place | one shared **ROX/RWX** PVC, N readers |

## Reference: how NVCA does NVMesh today

NVCA's model-cache (`pkg/storage/modelcache.go`, `doModelCacheNVMesh`) is the
zero-copy template:

1. **Primary PV** holds the populated cache; its
   `PersistentVolumeReclaimPolicy` is set to **`Retain`**
   (`finalizePrimaryPVOnSuccessfulInit`) so deleting the writer PVC never
   destroys the data.
2. For each consumer, a **secondary PV** is created as a `DeepCopy` of the
   primary with:
   - `AccessModes = [ReadOnlyMany]`
   - `Spec.CSI.VolumeHandle` rewritten by `updateSecondaryPVVolumeHandle` (an
     NVMesh-specific transform that flags the handle as a shared **read-only**
     attach of the *same* underlying volume),
   - `Spec.ClaimRef` pre-bound to the RO PVC it will serve, and `Status` cleared.
3. A matching **RO PVC** is created; the pre-set `claimRef` makes it bind to that
   exact secondary PV (static binding, no provisioner copy).

Net: one backing NVMesh volume, attached read-only to many pods, **zero bytes
copied** at promote or fan-out time. This is strictly better than snapshot+clone
when the storage supports it.

## Design: a `Promoter` strategy + a `StorageProfile`

Split `PerCapturePVCBackend` into **agnostic orchestration** (unchanged) and a
pluggable **`Promoter`** that owns the storage-specific transition and the
restore-side consume step.

### Interface (`internal/checkpointstore`)

```go
// Promoter turns a populated, detached writer PVC into the durable
// restore artifact for a content hash, and produces the pod-spec
// fragments restore pods use to consume it. One implementation per
// storage strategy; selected per-StorageClass via StorageProfile.
type Promoter interface {
    // Promote is called once per hash, by the lease winner, after the
    // write phase + waitForDetach. writerPVC is rwx-<hash> (RWO,
    // populated, now detached). It returns once the durable artifact
    // for `hash` exists (snapshot ready / primary PV retained / etc).
    Promote(ctx context.Context, p PromoteInput) (PromoteResult, error)

    // MountSpec returns the Volume + VolumeMount a restore pod uses to
    // read the artifact for `hash`. Strategies that fan out per-pod
    // (RWO clone) create the per-pod artifact here, keyed by vol.PodUID;
    // shared-ROX / shared-volume strategies return the one shared claim.
    MountSpec(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error)

    // Delete reclaims every artifact this strategy created for `hash`.
    Delete(ctx context.Context, hash, namespace string) error

    // Caps advertises capabilities for startup validation + logging.
    Caps() StorageCaps
}

type PromoteInput struct {
    Hash          string
    Namespace     string
    WriterPVCName string   // rwx-<hash>, RWO, populated + detached
    SizeBytes     int64
}

// PromoteResult tells the orchestrator how restore consumes the result,
// so it can set pvc_promote_state + the catalog rox_name correctly.
type PromoteResult struct {
    // SharedClaimName is the single ROX PVC all restore pods mount
    // (snapshot-clone-ROX, shared-volume). Empty for per-pod strategies.
    SharedClaimName string
    // PerPod is true when each restore pod gets its own clone created
    // lazily in MountSpec (RWO-only storage, e.g. EBS). The catalog
    // stores the snapshot/dataSource handle instead of a shared PVC.
    PerPod bool
    // ReusedWriterVolume is true when no copy happened (shared-volume) —
    // purely informational for metrics/logs.
    ReusedWriterVolume bool
}

type StorageCaps struct {
    Snapshots     bool // CSI VolumeSnapshotClass available
    ReadOnlyMany  bool // SC can bind a single volume ROX to many nodes
    SharedVolume  bool // same backing volume can be RO-attached N times
    Strategy      string // "snapshot-clone" | "shared-volume" | "copy-per-pod"
}
```

`Backend.Mount` (already in the interface) delegates to `Promoter.MountSpec`;
`Backend.Delete` delegates to `Promoter.Delete`. `Put` keeps the lease, write
phase, `waitForRWXDetach` (storage-agnostic — VolumeAttachment is core CSI), and
state machine; it replaces the inline `snapshotAndClone` call with
`promoter.Promote(...)` and records `PromoteResult` on the catalog.

### Implementations

1. **`SnapshotClonePromoter`** (default; Hyperdisk-ML, EBS, any CSI with a
   VolumeSnapshotClass). `Promote` = today's `VolumeSnapshot` → readyToUse; the
   snapshot is the durable dataSource. Then:
   - **ROX-capable SC** (`Caps.ReadOnlyMany`, e.g. Hyperdisk-ML): clone one
     `rox-<hash>` ROX PVC from the snapshot → `SharedClaimName`. *(today's path,
     unchanged.)*
   - **RWO-only SC** (`!ReadOnlyMany`, e.g. EBS gp3): `PerPod=true`. `MountSpec`
     lazily clones a fresh `rox-<hash>-<podUID>` **RWO** PVC from the snapshot for
     each restore pod. Costs one volume per pod (acceptable — EBS has no shared
     mode), but works where ROX is impossible.

2. **`SharedVolumePromoter`** (NVMesh, CephFS-by-handle, any `Caps.SharedVolume`).
   `Promote` = set the writer PV's reclaim policy to `Retain`, record its
   `CSI.VolumeHandle`; **no snapshot, no copy**. `MountSpec` = create a static
   secondary PV (DeepCopy of primary, `ReadOnlyMany`, `ClaimRef` pre-bound to a
   per-consumer RO PVC, `VolumeHandle` rewritten by a pluggable
   `VolumeHandleTransform`) + the matching RO PVC — mirroring NVCA's NVMesh path.
   A `nil` transform = handle reused verbatim (filesystems like CephFS that need
   no per-attach handle change).

3. **`CopyPerPodPromoter`** (last resort; RWO, no snapshots). `Promote` is a
   no-op marker; `MountSpec` provisions a fresh RWO PVC per pod and copies bytes
   in via the existing mount-holder Copier. Universal but expensive — in practice
   we'd prefer to just **not** offer L2 here and let the L3/L4 cascade serve
   restore. Included for completeness; ship only if a customer needs it.

### Selection: default SC → `.provisioner` → profile map (built-in ⊕ ConfigMap)

The agent resolves the strategy at startup from the **L2 StorageClass**
(`agent.l2.storageClass`). Note this is **not** the workload/function SC: on GKE
the function SC is `nvcf-sc` (`pd.csi.storage.gke.io`, `type: pd-ssd`,
RWO/low-throughput), whereas L2 fan-out wants a ROX-capable, high-throughput SC —
today `hyperdisk-ml` (`pd.csi.storage.gke.io`, `type: hyperdisk-ml`,
`provisioned-throughput-on-create: 10000Mi`). The two share a provisioner; the L2
SC is configured separately and defaults to a ROX-capable class, never `nvcf-sc`.

1. **Read the L2 SC object.** `GET storageclass/<l2-sc>` → take `.provisioner`
   **and `.parameters.type`**. The provisioner alone is ambiguous on GKE:
   `pd.csi.storage.gke.io` backs both Hyperdisk-ML *and* regular PD (pd-ssd,
   pd-balanced) — `.parameters.type` is the real discriminator. Also read
   `volumeBindingMode` + `.allowedTopologies` (sizing/topology).
2. **Look up the profile** by a **composite key** `provisioner[/type]` — the
   `/type` segment is used when the driver multiplexes volume types (currently
   `pd.csi.storage.gke.io`); bare provisioner otherwise. Resolve against a
   **layered map**:
   - **built-in default table** (compiled in) — every provider we've validated
     works with zero config;
   - **`nvsnap-storage-profiles` ConfigMap** (in nvsnap-system) — overlays/extends
     the built-in table per cluster, editable with `kubectl edit cm` and **no
     agent rebuild** (same pattern as the `nvsnap-cachedir-env` ConfigMap, #244).
     A ConfigMap entry for a provisioner key wins over the built-in.
3. **Construct the Promoter** from the resolved profile; log the SC name,
   provisioner, resolved strategy + caps at startup so an operator sees exactly
   what L2 will do. If no profile matches and nothing can be auto-derived →
   **disable L2 with a loud log** (restore falls back to the L3/L4 cascade) rather
   than guess wrong.

Why a profile map and not pure SC introspection: `.provisioner` gives the driver,
but two things it can't tell us must live in the profile —
- **snapshotClass** is a cluster-specific object name, not derivable from the
  driver (discover by listing VolumeSnapshotClasses for the driver as a fallback,
  but prefer the explicit value);
- **ROX-capability** isn't a pure function of the driver (depends on SC
  params/mode) — encode the known answer per provisioner.

```yaml
# nvsnap-storage-profiles ConfigMap — overlays the built-in table.
# Keyed by CSI provisioner (StorageClass.provisioner). Edit to add a
# backend or correct a detail without rebuilding the agent.
data:
  profiles.yaml: |
    # Key = provisioner[/parameters.type]. The /type segment
    # disambiguates drivers that multiplex volume types (pd.csi).
    pd.csi.storage.gke.io/hyperdisk-ml:    # Hyperdisk-ML — ROX multi-attach
      strategy: snapshot-clone
      snapshotClass: hdml-images-snapshot-class
      readOnlyMany: true
    pd.csi.storage.gke.io/pd-ssd:          # regular PD — RWO, no fan-out ROX
      strategy: snapshot-clone
      snapshotClass: csi-gce-pd-snapshot-class
      readOnlyMany: false                  # ⇒ per-pod clone
    ebs.csi.aws.com:                       # AWS EBS — RWO only
      strategy: snapshot-clone
      snapshotClass: ebs-snapshot-class
      readOnlyMany: false                  # ⇒ per-pod clone
    nvmesh-csi.excelero.com:               # NVMesh — zero-copy (xfs block)
      strategy: shared-volume
      volumeHandleTransform: nvmesh
      mountOptions: [ro, norecovery, nouuid]  # xfs RO multi-mount needs nouuid
    efs.csi.aws.com:                       # EFS — shared RWX filesystem
      strategy: shared-volume
      volumeHandleTransform: none
    filestore.csi.storage.gke.io:          # Filestore — shared RWX filesystem
      strategy: shared-volume
      volumeHandleTransform: none
```

Lookup tries the composite `provisioner/type` key first, then falls back to the
bare `provisioner` key, then to auto-derivation (snapshot class discovery), then
disables L2.

The built-in table carries these same defaults so a stock install needs no
ConfigMap; the ConfigMap exists for new/3rd-party backends and overrides.

## What stays put (no change)

- Hash-keyed Lease serialization, single-copy mount-holder write phase, the
  `pending→writing→snapshotting→ready` state machine names (the
  `snapshotting` state generalizes to "promoting"), namespace scoping
  (nvsnap#82), `waitForRWXDetach` durability barrier (nvsnap#105 — core CSI,
  needed by every block strategy), the `nvsnap-l2-wait` restore-side gate.
- The restore webhook still calls `Backend.Mount`; only the artifact it returns
  differs (shared claim vs per-pod clone vs static-PV claim).

## Migration / rollout

1. Extract `Promoter` interface; move current `snapshotAndClone` +
   `Mount` body into `SnapshotClonePromoter` verbatim (behavior-preserving
   refactor; existing tests pass unchanged).
2. Add `SharedVolumePromoter` + the `VolumeHandleTransform` plug (port the NVMesh
   handle transform from NVCA `updateSecondaryPVVolumeHandle`). e2e on the NVMesh
   cluster.
3. Add the RWO-only per-pod branch to `SnapshotClonePromoter`; e2e on EBS.
4. `StorageProfile` config + auto-detect table + startup validation.

Each step is independently shippable; step 1 is a pure refactor that unblocks the
rest.

## Open questions for review

- ~~**Per-pod EBS clones**~~ — **resolved**: ship an **EFS** profile
  (`shared-volume`, one filesystem, N readers) as the recommended AWS fan-out
  path; EBS per-pod-clone remains the fallback when no RWX filesystem is
  available.
- ~~**GC for shared-volume**~~ — **resolved**: the existing retention controller
  owns teardown. On a hash's retention expiry it deletes the secondary PVs/PVCs
  first, then the primary PV (whose `Retain` policy otherwise orphans it), which
  releases the backing CSI volume. No new GC loop.
- ~~**Capability auto-detect vs explicit**~~ — **resolved**: read the default SC
  (`nvcf-sc`) `.provisioner`, resolve against a built-in profile table overlaid
  by the `nvsnap-storage-profiles` ConfigMap; disable L2 (fall to cascade) when
  no profile matches rather than guess. See "Selection" above.

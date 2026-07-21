# SDD: Central Model Cache Service

## Status

Implemented in NVCA. This document describes the model cache design as built in
`pkg/storage` (the model cache StorageRequest controller), `pkg/webhook`
(workload volume injection), and `internal/metrics` (observability).

## Goal

A function or task can declare a model cache by `cacheHandle`. The first workload
that needs a given handle downloads the model once; every later workload for the
same handle, in any namespace, attaches the already-downloaded copy read-only
instead of downloading again. Caching is best-effort: if the cache cannot be
provisioned the workload still runs, just without a shared cache.

The service is folded into NVCA rather than run as a standalone operator. It
reuses NVCA's existing machinery: a control namespace, a per-cacheHandle
StorageRequest of type `modelcache`, a single-writer lease, an init/writer Job,
and the mutating webhook that mounts the cache into workload pods.

## Terminology

- cacheHandle: content hash identifying a model cache, supplied in the request
  `CacheLaunchSpecification` (`cacheHandle`, `cacheSize`).
- Control namespace: `nvca-modelcache-init`. Holds writer Jobs, the init lease,
  backend infrastructure, and durable markers.
- Backend: the storage mechanism used to hold and share the cache. One of
  `nvmesh`, `sharedfs`, `samba`, `ephemeral`.
- Writer: the single init Job that downloads the model into the backend.
- Reader: the per-namespace read-only volume a workload mounts.
- Durable marker: a cluster-scoped object whose existence means "this handle is
  populated", used so any namespace and any agent restart can detect a populated
  cache without depending on in-memory state.

## Backend selection

The miniservice reconciler selects a backend once per request via
`SelectHelmCacheBackend` (`pkg/storage/cachebackend.go`) and stamps it on the
`StorageRequest.spec.modelCache.backend`. Selection is deterministic on cluster
state and feature flags:

1. `CachingSupport` disabled: no cache (`none`).
2. `nvcf-sc-30` StorageClass exists (NVMesh 3.x): `nvmesh`.
3. `nvcf-miniservice-sc` StorageClass exists (operator-provided third-party shared
   storage): `sharedfs`.
4. `HelmSharedStorage` flag enabled: `samba`.
5. Otherwise: `ephemeral`.

NVCA never creates `nvcf-miniservice-sc`. That StorageClass is exclusively the
operator's signal that third-party shared storage is present (branch 3 above).
The Samba backend (branch 4) is self-contained and creates no StorageClass.

## Lifecycle shared by all shared backends

The shared backends (nvmesh, sharedfs, samba) share one lifecycle; only the
backing store and the per-namespace reader differ. The ephemeral backend is a
per-pod fallback described separately.

### Populate (single writer)

1. The reconciler resolves the request into a writer Job and a read-write cache
   PVC (`findAndDecodeCacheArtifacts`), sized from the payload `cacheSize`.
2. A per-cacheHandle lease (`modelcache-init-<handle>`) elects a single writer.
   The lease holder creates the writer Job and the read-write PVC in the control
   namespace; non-holders wait.
3. On writer success the reconciler records a durable marker (see below) and
   tears down the temporary initialization resources: the writer Job, its
   pods, and the lease (`cleanupInitModelCache`). The read-write PVC is also
   deleted for nvmesh and samba, but retained for sharedfs, where it is
   itself the durable marker. The marker survives.

### Durable markers

The marker is what makes a cache reusable across namespaces and across agent
restarts. It differs by backend because the reader-attach mechanism differs:

- nvmesh: the writer's bound PV is retained (relabeled with
  `nvca.nvcf.nvidia.io/modelcache-primary-pv`, reclaim policy Retain) as the
  primary PV. Secondary reader PVs are copies of it with the CSI VolumeHandle
  rewritten per namespace.
- samba and sharedfs: a `nvca.nvcf.nvidia.io/modelcache-populated=true` label on
  a durable per-handle PVC (the Samba backing PVC for samba; the retained writer
  PVC for sharedfs). Readers gate on this label.

Consumers check the marker before deciding to run init. If it exists they skip
the writer entirely and attach a reader. This is deliberately not dependent on
the init lease or any in-memory fan-out, both of which are lost on restart.

### Reader attach (per namespace)

- nvmesh: a secondary PV (copy of the primary, VolumeHandle rewritten for the
  namespace) plus a read-only PVC.
- samba: a static SMB CSI PV pointing at the per-handle Samba share, read-only,
  plus a read-only PVC.
- sharedfs: a read-only PVC on the shared class; the class itself shares data
  across namespaces, so no per-namespace PV plumbing is needed.

## Samba backend (Samba over NVMesh)

Selected when no NVMesh 3.x or operator shared storage exists but
`HelmSharedStorage` is on. NVMesh block storage is ReadWriteOnce, so it cannot be
shared read-only across namespaces directly. The Samba backend re-exports an
`nvcf-sc` volume over SMB, which supports ReadWriteMany and ReadOnlyMany.

Each cacheHandle gets its own Samba server and its own backing volume. There is
no single shared server or global data PVC: a single fixed-size volume cannot be
sized for an unknown, growing set of models, and hot-adding per-handle volumes
to one shared server would force pod restarts that drop every other handle's live
mounts. Per-handle servers isolate lifecycle and sizing and are reclaimed
independently.

Per cacheHandle (`cachebackend_samba.go`, `EnsureSambaModelCacheInfra`):

- Backing PVC `samba-<cacheHandle>` on `nvcf-sc`, sized to the request
  `cacheSize`. Its existence is the reuse signal; its
  `modelcache-populated` label is the safe-to-attach signal.
- Deployment `samba-<cacheHandle>` mounting that PVC, exporting a single SMB
  share (`cache`).
- Service `samba-<cacheHandle>` for stable in-cluster DNS.
- NetworkPolicy `samba-<cacheHandle>` restricting ingress to the SMB port.
- Shared read-write and read-only credentials Secrets (reused across handles).

The writer mounts the share read-write over static SMB CSI and populates it; per
namespace readers mount the same share read-only as a distinct unprivileged user.
Resource names are `samba-<cacheHandle>`; when a handle would exceed the 63
character name limit a stable hashed suffix is used.

## Workload volume injection

The mutating webhook (`pkg/webhook/helm_storage_webhook.go`) injects the cache
into workload pods. When a pod carries the
`nvca.nvcf.nvidia.io/storage-modelcache-pvc-name` annotation the webhook adds a
`model-data` volume bound to the read-only cache PVC and mounts it at the model
paths.

The shared-storage volumes use a drop-and-re-add pattern on every admission. The
model cache volume participates in the same drop-and-re-add
(`getModelCacheDropReservedFunc`) so the post-admission volume and mount order is
deterministic and identical across CREATE and UPDATE. Without this, a controller
that re-submits an already-mutated pod (for example the KAI scheduler pod-grouper)
would produce a reordered volume list, which the API server rejects as an
immutable-field change, leaving the workload pod unschedulable.

## Ephemeral backend (per-pod fallback)

When no shared backend is available (`ephemeral`), the webhook injects a
`model-data` emptyDir and a model-cache-init init container that downloads the
model into it. Workload containers see the cache at the same mount paths as the
shared path, so the workload is backend-agnostic. There is no cross-namespace
sharing in this mode.

## Garbage collection

A periodic sweep (`cleanupIdleModelCaches`) reclaims caches that no active
StorageRequest references and that have been idle past
`ModelCacheIdlePeriod`:

- nvmesh: retained primary PVs are deleted.
- samba: per-handle server (Deployment, Service, NetworkPolicy) and backing PVC
  are deleted (`DeleteSambaModelCacheInfra`), keyed on a last-referenced
  annotation updated whenever a consumer attaches.

Per-StorageRequest deletion (`doCleanupModelCacheNVMesh`) removes that request's
own reader objects but never the shared per-handle backing store, which is kept
for reuse and reclaimed only by the idle sweep.

## Non-blocking volume detach

Cleanup must delete the writer read-write PVC only after its volume detaches, but
the model cache controller runs a single reconcile worker. The detach check is a
single-shot probe (`isVolumeDetached`): if the volume is still attached the
cleanup returns a short requeue instead of polling in-reconcile. This prevents
one stuck cleanup from starving every other StorageRequest, including the
terminating-namespace finalizer escape hatch.

## Observability

Metrics (`internal/metrics`), all labeled by `backend`:

- `nvca_model_cache_backends` (gauge): caches currently provisioned per backend.
  Answers "how many Samba caches exist". Refreshed by the idle sweep.
- `nvca_model_cache_backend_selected_total`: backend chosen per request.
- `nvca_model_cache_populate_total`: the single-writer download ran.
- `nvca_model_cache_reuse_total`: an existing cache was attached without a
  download.
- `nvca_model_cache_reclaimed_total`: idle caches reclaimed by GC.
- `nvca_model_cache_result_total`: success or failure by failure reason and
  backend.

Tracing: `nvca.modelcache.reconcile` span per request (with a
`nvcf.modelcache.backend` attribute) and a `nvca.modelcache.samba.ensure_infra`
child span around per-handle Samba provisioning.

## Known gaps and fast-follows

- sharedfs reader binding assumes the class exposes one shared filesystem:
  separately provisioned PVCs on the same StorageClass do not share data on
  provisioners that create per-claim access points, subvolumes, or
  directories (EFS access points, CephFS subvolumes, some NFS provisioners).
  Classes backed by a single share (for example an SMB class pointing at one
  server share) work. The capability probe validates bindability, not
  data-sharing; the fast-follow is a write-through-one-claim /
  read-through-another probe, and deriving reader PVs from the writer's bound
  volume where the driver supports it.
- The per-handle Samba infrastructure is create-once: image, resource, and
  cache-size changes do not reconcile onto an existing server or expand its
  backing PVC.
- The ephemeral init env forwards the full launch environment; it should be
  narrowed to the variables the model-cache-init container actually consumes.

## Key invariants

- NVCA never creates `nvcf-miniservice-sc`.
- Exactly one primary PV per handle for nvmesh; exactly one backing PVC per
  handle for samba.
- The Samba backing PVC is not labeled with the cache-handle label, so the
  init-writer cleanup (which deletes handle-labeled PVCs) never removes it.
- Reuse and safe-to-attach are decided from durable cluster state, never from
  in-memory fan-out.
- Webhook volume injection is order-stable across CREATE and UPDATE.

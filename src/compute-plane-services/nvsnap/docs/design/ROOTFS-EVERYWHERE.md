# Design: Rootfs-Everywhere

Status: implementing v0.0.49
Owner: Balaji Ganesan

## Problem

The NvSnap agent supports two capture paths:

- **CRIU + cuda-checkpoint** (process+GPU state dump)
- **Rootfs+OverlayFS** (container fs snapshot, cold-load model on restore)

CLAUDE.md rule 20 picks per (arch × GPU-count × NIM backend). CRIU is default for x86 single-GPU non-NIM. Rootfs is the fallback for everything CRIU can't handle (Riva/Triton NIMs, multi-GPU, arm).

This split has cost us:

- CRIU bugs in production over the last quarter: TCP_ESTABLISHED restore, binfmt_misc, CUDA plugin stage-2 failures, cuda-checkpoint serialization on 38-PID NIMs, libcudart wall on multi-GPU. Each one fixed; each one bled days.
- Restore-side fragility: CRIU restored processes inherit the captured pod's IP — Hyperdisk-ML WaitForFirstConsumer interactions, NVCA pod hardening (no CAP_SYS_ADMIN), function-pod securityContext — all hostile to CRIU restore.
- Multi-GPU and arm have always taken the rootfs path. Rootfs proved out at scale (vllm-70b TP=4 fan-out, whisper, mistral). Cold-load on restore (~30s for mistral) is acceptable given the operational simplicity.

The right move: make rootfs the single production path. Keep CRIU as code (rollback escape hatch via `NVSNAP_DEFAULT_CAPTURE_PATH=criu`), but design for rootfs being THE path.

`v0.0.48` flipped the **agent default** to rootfs via `RootfsIsDefault()`. The rootfs path itself was still half-built. This doc covers the rest.

## What's broken today (post-v0.0.48)

Caught during GCP-H100-a benchmarking on 2026-06-09:

1. **Rootfs path doesn't produce an L2 PVC.** `rootfsonly.Capturer.Backend` is wired as `ConfigMapBackend(LocalBackend, cmNs)`. `PerCapturePVCBackend` (writer Job + rwx PVC + snapshot + rox PVC) is not in the chain. Result: no fan-out artifact. Multiple restored pods can't mount one rox PVC.
2. **Manifest ConfigMap lands in `nvsnap-system`** instead of the source pod's namespace. Cross-namespace reads for restore. Breaks multi-tenant isolation. The agent's `cfg.CMNamespace` is a global default, not per-capture.
3. **Rootfsonly Watcher polls** on a 30s interval. Hook B → label → watcher pickup observed at ~60s tail. Should be informer-driven on `nvsnap.io/capture=true` label add/update.
4. **NVCA Hook B warmup buffer defaults to 10s.** Treats `0` as "use default 10s". A pod's readiness probe already guarantees ready; adding 10s captures stale CUDA graphs and adds latency for no benefit.
5. **nvsnap-server's 422 redirect handoff** is in v0.0.15 (MR !78). Required because agent returns 422 with `redirect: rootfs` body; nvsnap-server now calls `runRootfsCheckpoint` which labels the pod for the watcher. Already shipped; included here for completeness.
6. **nvsnap-server ClusterRole missing `pods/patch`** verb. Surfaced by (5) when nvsnap-server tried to label workload pods in the workload namespace. Patched live on GCP-H100-a; helm chart change pending in v0.0.49.

## Target architecture

### Single path

All captures go through rootfs. The CRIU path code stays in `internal/agent/checkpoint.go` for rollback (`NVSNAP_DEFAULT_CAPTURE_PATH=criu` reaches it). No new production caller. CRIU deprecation timeline is out of scope for this doc — we'll measure rootfs in production for one quarter, then prune CRIU.

### Backend chain

`rootfsonly.Capturer.Backend` becomes a composite that, on `Put(hash, sources, manifest)`, does:

1. `LocalBackend.Put` — files into the agent's host hostPath cache. Unchanged.
2. `ConfigMapBackend.Put` — manifest CM named `nvsnap-capture-<hash>` in the **source pod's namespace** (from `manifest.SourcePodMeta.namespace`).
3. `PerCapturePVCBackend.Put` — writer Job mounts `/host`, tars host cache → rwx PVC. VolumeSnapshot. Rox PVC. All in the source pod's namespace.
4. `PostCommit` hook (already present) uploads to nvsnap-blobstore for L4 cross-cluster restore.

Composition mechanism: a `ChainBackend` wrapper in `internal/checkpointstore` whose `Put` calls each child in order, short-circuiting on first error after rolling back successful children. `Stat`/`Get`/`Mount`/`Delete` dispatch to whichever child owns the artifact (cmregistry pattern, generalized).

### Watcher

`rootfsonly.Watcher` switches from poll loop to `cache.SharedIndexInformer`:

- Watches `Pods` cluster-wide with field selector `metadata.labels.nvsnap.io/capture=true`
- AddFunc + UpdateFunc handlers enqueue a workqueue
- Worker pops + dispatches to `Capturer.Capture`
- Periodic resync (5 min) catches missed events

Latency target: label → capture start ≤ 2s.

### Warmup buffer

In `pkg/apis/nvcf/v1/nvcfbackend_types.go`:

- `DefaultNvSnapWarmupBufferSeconds = 0` (was 10)
- `NvSnapConfig.Complete()`: `0` means literal zero, not "fall back to default". The previous behavior conflated those two.
- Cluster operators who want a buffer can set `spec.nvsnap.warmupBufferSeconds` explicitly.

Honest readiness probe is the contract. If `/health` returns 200 prematurely, fix the probe (out of scope here).

### Namespace rules

- Manifest CM: source pod's namespace
- rwx/rox PVCs: source pod's namespace (already correct for L2, per nvsnap#82)
- Cache directory on host: stays in agent hostPath (per-node, not per-namespace)
- Agent ClusterRole: needs ConfigMap CRUD cluster-wide (the agent's existing access already includes this for nvsnap-system; verify it generalizes — yes, `configmaps` rule in agent-rbac.yaml is cluster-scoped)
- nvsnap-server ClusterRole: add `pods/patch` (already applied tactically, helm chart change in v0.0.49)

### Restore

No change. Webhook injects the rox PVC volume mount on pods with `nvsnap.io/restore-from`. WaitForFirstConsumer binds the rox PVC on first consumer's scheduling (per the L2 fix in v0.0.47 / MR !76).

## Change table

| Component | Today | v0.0.49 |
|---|---|---|
| `rootfsonly.Capturer.Backend` | `Local → ConfigMap` | `Local → ConfigMap → PerCapturePVC` (`ChainBackend`) |
| Manifest CM namespace | `cfg.CMNamespace` (nvsnap-system) | source pod's namespace (`manifest.SourcePodMeta.namespace`) |
| rwx/rox PVC namespace | source pod's namespace | (unchanged) |
| Watcher trigger | 30s poll | informer event on label |
| NVCA `DefaultNvSnapWarmupBufferSeconds` | 10 | 0 |
| NVCA `NvSnapConfig.Complete()` warmup semantics | `0 → 10` | `0 → 0` |
| Default capture path | rootfs (kept from v0.0.48) | (unchanged) |
| L2 PVC for rootfs | Not wired | Always-on, in source ns |
| nvsnap-server ClusterRole `pods/patch` | Applied tactically | In helm chart |
| Per-function backoff in NVCA | Per-fvID counter | (unchanged — separate design needed for path-aware) |

## Files touched

| File | Change |
|---|---|
| `internal/checkpointstore/chain.go` (new) | `ChainBackend` impl: composes N backends, sequential Put with rollback. |
| `internal/checkpointstore/chain_test.go` (new) | Unit tests for chain Put / Get / Stat / Delete / rollback. |
| `internal/checkpointstore/cmregistry.go` | `ConfigMapBackend.Put` reads namespace from `manifest.SourcePodMeta` instead of constructor default. Constructor namespace becomes the fallback for manifests without SourcePodMeta. |
| `internal/agent/rootfsonly_integration.go` | `buildAgentBackend` returns `ChainBackend{Local, ConfigMap, PerCapturePVC}`. PerCapturePVCBackend gets the L2 config (StorageClass, SnapshotClass, etc.) from agent flags. |
| `internal/rootfsonly/watcher.go` | Replace poll loop with `informers.NewSharedInformerFactoryWithOptions` + label selector workqueue. Keep 5min resync as safety net. |
| `internal/rootfsonly/watcher_test.go` | Update tests to exercise the informer event path. |
| `pkg/nvca/nvsnap/reconciler/reconciler.go` (nvca-nvsnap) | `DefaultNvSnapWarmupBufferSeconds = 0` |
| `pkg/apis/nvcf/v1/nvcfbackend_types.go` (nvca-nvsnap) | `NvSnapConfig.Complete()`: `0` means literal zero. Update doc comment. |
| `deploy/helm/nvsnap/templates/server.yaml` | ClusterRole: add `patch` to `pods` verbs (mirror today's live patch). |
| `scripts/versions.sh` | `NVSNAP_APP_VERSION` v0.0.48 → v0.0.49. |

## Acceptance criteria

1. Fresh NVCA-driven function deploy on GCP-H100-a:
   - Pod Ready 2/2 → catalog row `Phase=Completed` with hash + rox-`<hash>` PVC in source pod's namespace, in **≤ 90s** (was ≥120s with poll + warmup + ConfigMap+PVC roundtrip).
   - Manifest CM lives in the workload namespace, not `nvsnap-system`.
2. Subsequent deploys of same fvID:
   - Hook A finds catalog row via content-addressed lookup → stamps `nvsnap.io/restore-from`.
   - Restored pod boots without cold-load; mounts the rox PVC on first attach.
3. Two-pod fan-out (`test-bench.sh rootfs --fanout=2` or NVCA scale-out):
   - Both pods mount the same rox-`<hash>` PVC.
   - Both serve inference.
4. No leaks: `nvsnap-writer` Job auto-deletes after 60s (TTL from v0.0.48); rwx PVC deleted after snapshot.
5. Unit tests pass: `go test ./internal/checkpointstore/... ./internal/rootfsonly/... ./internal/agent/...`.
6. v0.0.49 image deployed on GCP-H100-a; end-to-end above runs against the deployed image.

## Out of scope

- Path-aware backoff in NVCA (`CFS.status.attempts` per capture-path). Today's counter is per-fvID and can incorrectly gate the rootfs path on prior CRIU failures. Tracked as a follow-up; manual CFS delete works as the reset.
- Multi-zone L2 fan-out (Hyperdisk-ML is zonal; cross-zone restore needs L4 cascade). Task #163.
- Zero-copy CRIU dump direct into PVC (task #173). Moot if CRIU is deprecated.
- UI updates to surface manifest namespace.
- NvSnap Server pre-creating GPUCheckpoint CRs (today's Failed → swallowed-retry flow already handles this).
- Deleting CRIU code path — separate decision after one production quarter on rootfs.

## Migration

- v0.0.49 image rolls onto every nvsnap cluster (GCP-H100-a, future GCP-H100-b) via helm chart upgrade. No config changes required.
- Existing catalog rows + manifest ConfigMaps in `nvsnap-system` namespace remain readable. Restore-side code reads from whichever namespace the manifest lives in via SourcePodMeta resolution; old rows fall back to the constructor's default namespace.
- NVCA operator gets v0.0.49 of nvca-nvsnap (warmup default flip). The change is backward-compatible: pre-existing NVCFBackend resources with `warmupBufferSeconds: 0` will now actually mean zero buffer (was 10s); operators wanting the old behavior set the value to `10` explicitly.

## Rollback

If v0.0.49 breaks production rootfs:

1. `kubectl set image ds/nvsnap-agent -n nvsnap-system agent=...:v0.0.48` — rolls back the agent, the bench you wanted today still works (rootfs default, just no L2 PVC).
2. `kubectl set image deploy/nvsnap-server -n nvsnap-system server=...:v0.0.15` — rolls back nvsnap-server. Already at this version.
3. If a specific cluster needs CRIU restored as default: `kubectl set env ds/nvsnap-agent -n nvsnap-system NVSNAP_DEFAULT_CAPTURE_PATH=criu`. The CRIU code stays in v0.0.49 binary.

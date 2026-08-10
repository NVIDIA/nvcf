# Rootfs restore via OverlayFS

Status: implemented
Created: 2026-06-04

## Problem

The rootfs capture path (multi-GPU x86 and all arm — see CLAUDE.md rule
20) snapshots a curated set of well-known cache dirs from the source
pod's overlay upperdir and serves them to restore pods via webhook-
injected hostPath bind mounts. Today those mounts are read-only:

- On local NVMe (Tier-1 / Local backend), kubelet enforces
  `volumeMount.readOnly: true`.
- On L2 PVC fan-out (Tier-2 ROX), Hyperdisk-ML mounts the block device
  RO at the CSI driver level; even setting `readOnly: false` in the
  pod spec wouldn't make the kernel allow writes.

Workloads that mutate paths inside the captured tree at runtime hit
errno 30 (EROFS) and crash. Concretely seen 2026-06-04 on
gpt-oss-120b TP=4 with vllm/vllm-openai:v0.20.0: vLLM's V1 engine
writes per-runtime tempfiles to
`/root/.cache/vllm/torch_compile_cache/.../inductor_cache/` during
cudagraph capture. vllm-70b on v0.11.2 didn't hit this because the
V0 engine rarely wrote to the cache at runtime — masked the bug.

Removing `/root/.cache/vllm` from the capture catalog
(`internal/rootfsonly/extract_paths.go`) is a one-line fix for the
immediate failure, but doesn't address the general case: any future
workload that writes to a captured cache dir will hit the same wall.

## Goal

A workload-agnostic way for restore pods to read from the captured
tree and freely write into the same paths at runtime, while:

- The captured tree stays read-only (sharable across N restore pods).
- Per-pod writes go to per-pod ephemeral storage (no cross-pod
  contamination).
- Works for both Tier-1 Local hostPath and Tier-2 ROX PVC sources.
- Doesn't require kernel features beyond mainline overlay (4.x+).

## Design

Per-restore-pod overlay union, mounted on the host by the agent.

```
+-------------------------- Pod's view ------------------------------+
| /root/.cache/vllm/    <-- bind mount of /var/lib/nvsnap/overlays/    |
|                           <pod-uid>/<captured-path>                |
|                                                                    |
| Reads fall through to lower; writes land in upper, transparently   |
+-------------------------------- ^ ---------------------------------+
                                  |
+--------- Host (nvsnap-agent prepared this overlay) ------------------+
| mount -t overlay overlay                                           |
|     -o lowerdir=<captured-tree>,                                   |
|        upperdir=<ephemeral-upper>,                                 |
|        workdir=<ephemeral-work>                                    |
|     /var/lib/nvsnap/overlays/<pod-uid>/<captured-path>               |
|                                                                    |
| lowerdir       /var/lib/nvsnap/cache/<hash>/<path>      (Tier-1)     |
|                /mnt/nvsnap-rox/<hash>/<path>            (Tier-2 ROX) |
| upperdir       /var/lib/nvsnap/overlays/<pod-uid>/upper/<path>       |
|                  (tmpfs-backed via emptyDir-equivalent on host)    |
| workdir        /var/lib/nvsnap/overlays/<pod-uid>/work/<path>        |
+-------------------------------- ^ ---------------------------------+
                                  |
+--------- Restore flow ---------------------------------------------+
| 1. Pod created with nvsnap.io/restore-from: <hash>                   |
| 2. Webhook (Mutating Admission) intercepts pod                     |
|    a) For each ExtractPath in capture manifest:                    |
|       - POST http://nvsnap-agent.<node-fqdn>:8081/v1/restore/overlay |
|         { pod_uid, capture_hash, extract_path, tier }              |
|       - Agent mounts overlay, returns mountpoint                   |
|    b) Replaces direct hostPath injection with                      |
|       hostPath of overlay mountpoint, readOnly=false               |
| 3. Pod runs. Workload writes freely.                               |
| 4. Pod terminates → agent's owner-ref watcher unmounts +           |
|    cleans up upper/work for that pod-uid.                          |
+--------------------------------------------------------------------+
```

### Why on the host, not in an init container?

OverlayFS mounted inside an init container is invisible to the main
container — Kubernetes gives each container in a pod its own mount
namespace by default. Sharing mount NS across containers is non-
standard and brittle. Doing the mount on the host (via the agent
DaemonSet, which is already privileged and present on every GPU node)
and then bind-mounting the result into the pod via hostPath is the
straightforward POSIX path that kubelet already supports.

### Why per-pod upper, not per-restore-target upper?

A captured tree may be restored on dozens of pods simultaneously
(L2 PVC fan-out). Sharing the upper layer across pods would re-create
the cross-pod contamination problem at the upper level. Per-pod uppers
(keyed by `pod-uid`) are the only thing that gives true isolation.

### Lifecycle

- **Prepare**: webhook calls agent at admission time; agent creates
  upper/work dirs, mounts overlay, returns the mountpoint. Idempotent
  on pod-uid: if mount already exists, return its path.
- **Use**: pod runs. Kernel handles the union transparently.
- **Cleanup**: agent watches its own mountpoints; on the K8s pod
  deletion event for `pod-uid`, `umount` the overlay and `rm -rf`
  upper+work. If agent crashed before cleanup, a startup sweep removes
  orphan mounts (no pod with that uid → unmount).

Cleanup is per-pod-uid, not refcounted: each overlay belongs to one
pod, no sharing.

## Out of scope

- OverlayFS *between* captured trees (e.g., compose multiple captures).
  We mount one captured tree per extract path; multiple extract paths
  → multiple independent overlays.
- Writeback of upper into the capture (i.e., updating the capture
  with per-pod runtime artifacts). Not useful: per-pod writes are
  scratch by definition.
- CSI driver implementation. The agent acts directly via the mount
  syscall; the K8s storage plane only sees the resulting hostPath.

## Implementation plan

1. **internal/agent/restoreoverlay.go** (new, ~150 LoC):
   - `PrepareOverlay(podUID, captureHash, extractPath, tier) (mountpoint, error)`
   - `CleanupOverlay(podUID) error`  (umount all overlays for pod-uid + rm upper/work)
   - `SweepOrphans(ctx, kubeClient) error`  (startup garbage collection)
2. **internal/agent/server.go** routes:
   - `POST /v1/restore/overlay` — prepare, returns mountpoint JSON
   - `DELETE /v1/restore/overlay/<pod-uid>` — manual cleanup hook
3. **internal/webhook/restore_entrypoint.go**:
   - Replace direct hostPath injection for ExtractPaths with the
     prepare-call → hostPath-of-mountpoint pattern.
   - `readOnly: false` on the volumeMount (because the overlay union
     IS writable; the underlying lower stays RO via OverlayFS, not
     via the volumeMount flag).
4. **internal/agent/pod_watcher.go** (extend existing):
   - Watch pod-delete events; call `CleanupOverlay(podUID)`.
5. **internal/rootfsonly/extract_paths.go**:
   - Re-add `/root/.cache/vllm` to the catalog now that the overlay
     handles writes. Comment with the bug history.
6. **Tests**:
   - Unit: mountpoint path generation, idempotency on repeat prepare.
   - Integration: real overlay mount with fake captured tree,
     write into upper, verify lower untouched.
   - E2E: gpt-oss-120b TP=4 rootfs round-trip via test-bench.sh —
     restore inference probe must succeed (today's blocker).

## Risks

- **mount(2) requires CAP_SYS_ADMIN**: agent runs privileged today,
  so we have it. No change.
- **OverlayFS over ROX block device**: needs verification. Hyperdisk-ML
  ROX mounts ext4 RO; OverlayFS supports any RO filesystem as lower.
  Will validate in the integration test before declaring the design
  complete.
- **Mount namespace propagation**: the agent's host mount points must
  propagate to kubelet's view so the hostPath bind-mount sees them.
  Standard for `/var/lib/nvsnap/*` which kubelet already sees (nvsnap
  checkpoints live there).
- **Inode exhaustion on /var/lib/nvsnap**: each pod creates an upper +
  work dir. For a 50-pod fan-out, ~100 dirs. Negligible.

## Acceptance

- gpt-oss-120b TP=4 PDF bench: restore succeeds, post-restore
  inference probe returns 200, recorded in docs/PDF-BENCH-RESULTS.md.
- vllm-small + e5-mistral CRIU path benches: no regression
  (single-GPU CRIU path is unaffected).
- vllm-70b TP=4 rootfs bench: no regression (the OverlayFS path
  replaces a working hostPath bind mount).

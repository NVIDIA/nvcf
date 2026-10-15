# Unified Capture Storage Design

Status: **Proposed** — coding starts 2026-05-06.

## Problem

Today the two capture paths use different storage backends:

| Path | Capture writes | Restore reads | Cross-node? |
|------|---|---|---|
| rootfs (multi-GPU) | per-capture PVC via gpdrox PV-flip | reader PVC | yes |
| CRIU (single-GPU) | hostPath `/var/lib/nvsnap/checkpoints` (root EBS on AWS) | hostPath same node | no |

This produces three concrete pains:

1. **Storage performance is whatever the node's root EBS volume is.** On AWS p5.48xlarge that's gp3 at ~125 MB/s baseline. Measured: 63 GB CRIU dump for nim-llama-8b takes ~8 min, and the bottleneck is filesystem throughput, not CRIU itself.
2. **CRIU artifacts are node-pinned.** Restore can only land on the node where capture happened. Demo's "fan-out across the cluster" story is rootfs-only.
3. **Two different lifecycle models** — one for hostPath checkpoints, one for PVC captures — means two reconcilers, two delete paths, two restore-entrypoint code paths.

The user direction: "default to block for all checkpoints/restores", "no hacks", "works for all paths".

## Design

A single storage abstraction shared by both paths.

### Tier 1 — durable, content-addressed (already exists)

The per-capture PVC pattern via `gpdrox` PV-flip stays. Every artifact lands here, keyed by content hash. The StorageClass `nvsnap-capture` is the integration point per cloud:

```
GCP    → pd.csi.storage.gke.io       pd-ssd   xfs   ROX-after-flip
AWS    → ebs.csi.aws.com             gp3      xfs   RWO-after-flip
Azure  → disk.csi.azure.com                   xfs   RWO-after-flip
NVMesh → nvmesh-csi.excelero.com                    ROX-after-flip
```

Reader access mode auto-detected from `SC.provisioner` at backend init (already shipped in v0.17.9).

### Tier 2 — capture writer (replaces hostPath for CRIU)

Both paths spawn a **per-capture writer Job** pinned to the source node, with the writer PVC mounted at `/dest`. Job runs `nvsnap-agent capture-write --type={criu|rootfs} ...`:

* **CRIU writer** — Job runs `criu dump` against the source pod's PID. Shares host PID + mount namespace via the same privileges the agent has today. CRIU's output stream goes straight to `/dest/criu/`. **One write, not two.**
* **Rootfs writer** — same as today's `nvsnap-agent capture-copy` flow. Already one-write.

The agent's existing `/v1/checkpoint` REST endpoint becomes thin: validates the request, derives the hash, calls `gpdrox.Backend.Put([]CaptureSource)` with the right `--type` plan. The backend spawns the Job. Same shape for both paths.

### Tier 3 — catalog + reconciler (already exists)

`ConfigMap` manifest registry. Reconciler already ingests rootfs captures from CMs into the DB; extending to CRIU requires no new code path because both paths produce the same shape of `Manifest` (hash + `SourcePodMeta` + size + `CapturedAt`). Only difference is the `Volumes` slice has a `criu` entry instead of `rootfs`/extract paths.

### Optional Tier 0 — node-local read cache (deferred)

Agent maintains an LRU cache on local NVMe of recently-read hashes, populated on first restore on a node. Restore-side accelerator only; never authoritative. Defer until measured need on hot-restore patterns.

## Speed knobs (independent of architecture)

Per-capture PVC throughput is now an SC parameter, not a node-level config. AWS example:

```yaml
parameters:
  type: gp3
  csi.storage.k8s.io/fstype: xfs
  iops: "16000"
  throughput: "1000"   # MB/s — gp3 max
```

8 min → ~1 min for 63 GB at 1000 MB/s. Costs more per disk-hour; that's an operator tradeoff knob, not a hidden hack.

For workloads where 1 GB/s isn't enough: switch SC to `io2` (up to 7 GB/s) or `Filestore`/`EFS` for parallel-stream RWX. All operator-level config; zero code changes.

## Restore path (single pattern)

Both CRIU and rootfs restores follow:

1. Customer-shape pod with `nvsnap.io/restore-from: <hash>` annotation.
2. Webhook resolves hash → `Manifest` → injects PVC reference + `volumeMounts` (with `subPath` for each captured directory) + `nodeAffinity` if backend reports `CapturedOnNodes`.
3. Restore-entrypoint (CRIU) reads from `/checkpoints` — that path is now a PVC mount, not hostPath. No code change needed in the entrypoint.

## Migration steps

1. **`nvsnap-agent capture-write` subcommand** — wraps either the existing CRIU dump RPC or `treecopy.Copier`. JSON plan via env var `NVSNAP_CAPTURE_PLAN`, same as today's `capture-copy`.
2. **Generalize `gpdrox.Put`** — add a `WorkloadKind` field on `CaptureSource` (rootfs, criu, …). The Job factory already takes the plan as base64 JSON; no shape change there.
3. **Server's CRIU flow rewritten** — `runDemoCheckpoint` calls the same `Backend.Put` rather than `agent /v1/checkpoint`. Agent's `/v1/checkpoint` endpoint stays for one release with a deprecation log line, then deletes.
4. **Restore manifest changes** — `/checkpoints` mount switches from hostPath to PVC reference. The webhook injection logic already handles PVC-shaped mounts.
5. **DB schema** — `Checkpoint.CheckpointPath` becomes optional / for legacy rows only. The hash is the canonical identifier (already the case for rootfs).
6. **Per-cloud StorageClass examples** — document the throughput knobs per cloud.

## What this gives

- **Single storage abstraction** for both paths.
- **Cross-cluster restore for both** (same as today's rootfs).
- **No hostPath dependency** anywhere in the capture/restore path. Survives node failures.
- **Tunable speed** via SC parameters, not hidden hacks.
- **No node-admin steps** needed (no DaemonSet RAIDs, no instance-store fiddling).

## Tradeoffs

- "Scratch is free" property of today's hostPath is gone. Every CRIU dump provisions a PVC. Cost: a few cents per checkpoint.
- Writer Job runs `criu dump` from a sibling pod against the source's PID. Already an established pattern (the agent does this; we move it into a per-capture Job).
- Existing hostPath checkpoints don't appear in the unified flow without a one-time backfill (small tool).
- Provisioning per-capture PVCs is slower than a hostPath write start — adds ~10s to every capture before the dump even begins.

## Estimated work

- Code + tests: ~1.5 days
- Demo retrofit + e2e validation on both clouds: ~half day
- Total: ~2 days, single PR

## Out of scope (separate work)

- Local NVMe cache layer (Tier 0).
- Snapshot-based ROX (per-cloud read-fan-out beyond what RWO + nodeAffinity already gives) — tracked as #46 in MEMORY.md.
- Cross-cloud capture portability (e.g. GCP capture restored on AWS) — different problem; needs driver-major / SKU compat catalog and is workload-specific.

## Related tasks

- **#47** CRIU artifacts on PV-flip storage — this design implements it.
- **#49** Cross-node fanout in `demoScaleOut` — works once both paths are PVC-backed and node-portable; falls out for free.

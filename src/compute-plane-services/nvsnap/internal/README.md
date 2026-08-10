<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# internal/

Repo-private Go packages (not importable by external modules). The bulk of
nvsnap's logic lives here; `cmd/` binaries wire these together.

## Package map

**Capture / restore core**
- [`agent/`](agent/) — checkpoint & restore orchestration; capture-path
  selection and backend detection.
- [`criu/`](criu/) — go-criu RPC wrapper (dump/restore, RPC field plumbing).
- [`cuda/`](cuda/) — cuda-checkpoint integration.
- [`rootfsonly/`](rootfsonly/) — rootfs/cachedir capture orchestrator + the
  warm-pod watcher.
- [`treecopy/`](treecopy/) — shared file-tree copier (sendfile + parallel walk).
- [`streamer/`](streamer/) — dormant streaming-compression path (see
  [THIRD-PARTY-FORKS](../docs/THIRD-PARTY-FORKS.md)).

**Storage / catalog**
- [`checkpointstore/`](checkpointstore/) — the Backend chain (Local → ConfigMap
  → per-capture PVC → blob store).
- [`blobstore/`](blobstore/) — disk-backed content-addressed blob store.
- [`objectstore/`](objectstore/) — object-store buckets (GCS) for cross-cluster.
- [`db/`](db/) — SQLite catalog (checkpoints, retention, audit).

**Control plane / platform**
- [`server/`](server/) — REST API handlers + web UI serving.
- [`webhook/`](webhook/) — mutating admission webhook (cachedir capture +
  restore injection).
- [`runtime/`](runtime/), [`containerd/`](containerd/) — container-runtime
  probing/integration.
- [`metrics/`](metrics/), [`tracing/`](tracing/), [`podtimings/`](podtimings/) —
  Prometheus metrics, OpenTelemetry, pod cold-vs-restored classification.

## Rules

Anything an external module must import belongs in [`../pkg/`](../pkg/), not
here. Keep package boundaries narrow; don't widen `internal/` to leak a type.

<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# internal/

Repo-private Go packages (not importable by external modules). The bulk of
nvsnap's logic lives here; `cmd/` binaries wire these together.

## Package map

**Capture / restore core**

- [`agent/`](agent/) — the node agent: checkpoint and restore orchestration,
  capture-method selection, CRIU group capture and restore of multi-pod
  instances, gpushare integration, the peer-cascade HTTP server, and the
  in-process admission webhook's wiring.
- [`criu/mountinfo/`](criu/mountinfo/) — /proc mountinfo parsing shared by capture and restore.
- [`cuda/`](cuda/) — cuda-checkpoint integration and GPU process discovery.
- [`rootfsonly/`](rootfsonly/) — rootfs/cachedir capture orchestrator + the
  warm-pod watcher.
- [`treecopy/`](treecopy/) — shared file-tree copier (sendfile + parallel walk).
- [`tarstream/`](tarstream/) — moves a directory tree between agents as an
  uncompressed tar that cannot escape its destination.
- [`hostlibs/`](hostlibs/) — per-architecture driver and libc library
  directories (amd64 and arm64).

**Models and admission**

- [`webhook/`](webhook/) — mutating admission webhook: cachedir capture,
  restore injection, CRIU restore placeholders, model-volume landings.
- [`modelid/`](modelid/) — which model artifact a GPU pod will download, and
  where it puts it.
- [`modelvolume/`](modelvolume/) — the per-model volume: written once, read
  by every other pod.
- [`election/`](election/) — picks one downloader per cache hash with a
  Kubernetes Lease when a chart starts many model workers at once.

**Storage / catalog**

- [`checkpointstore/`](checkpointstore/) — the rootfs Backend chain (Local → ConfigMap
  → per-capture PVC) and its storage promoters.
- [`objectstore/`](objectstore/) — content-addressed object-store tier (GCS)
  for cross-cluster replication.
- [`db/`](db/) — SQLite catalog (checkpoints, retention, audit).

**Control plane / platform**

- [`server/`](server/) — REST API handlers + web UI serving.
- [`runtime/`](runtime/), [`containerd/`](containerd/) — container-runtime
  probing/integration.
- [`manifests/`](manifests/) — the workload catalogue (`workloads.yaml`) and
  the restore manifests generated from it.
- [`metrics/`](metrics/), [`tracing/`](tracing/), [`podtimings/`](podtimings/) —
  Prometheus metrics, OpenTelemetry, pod cold-vs-restored classification.

## Rules

Anything an external module must import belongs in [`../pkg/`](../pkg/), not
here. Keep package boundaries narrow; don't widen `internal/` to leak a type.

<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Global Discovery Service end-to-end test on k3d

This directory contains the end-to-end test of the Global Discovery Service
(GDS) on local k3d clusters. `run.sh` builds the `gds-server` image from this
checkout, installs the `global-discovery` chart and drives the server over
REST. The server is `src/control-plane-services/global-discovery`; its REST
API is described in that module's `docs/api.md`.

The test is split into phases. Phase 1 runs today. Phase 2 (three clusters
with the MCS controller) and phase 3 (Stargate and Pylon through GDS) are
added as further phases of the same script; see
`docs-gds/implementation-plan.md`.

## Prerequisites

- Docker and `k3d` 5.x. `run.sh` creates k3d cluster `gds-e2e` when it does
  not exist.
- `kubectl`, `helm` 3.x, `curl` and `jq`.
- `bazel`, to build the image. `E2E_SKIP_BUILD=1` reuses the image the last
  build loaded into Docker.
- `bash` 3.2 or later. `run.sh` runs with the macOS default bash.
- Free local ports `28080` and `29091` for the port-forward
  (`E2E_HTTP_LOCAL_PORT`, `E2E_ADMIN_LOCAL_PORT`).

Each k3s node needs inotify instances on the Docker host. When other k3d
clusters run, the host default of 128 (`fs.inotify.max_user_instances`) can
be used up; the new node then never registers and `run.sh` stops with a hint
about it. Raise the limit on the Docker host, for example
`colima ssh -- sudo sysctl -w fs.inotify.max_user_instances=512`, or stop
clusters you do not need.

## Run

From the repository root:

```bash
make -C tests/e2e/global-discovery-k3d test          # leaves the cluster and release in place
make -C tests/e2e/global-discovery-k3d test-cleanup  # removes what the run created
```

Or directly:

```bash
tests/e2e/global-discovery-k3d/run.sh
E2E_CLEANUP=1 tests/e2e/global-discovery-k3d/run.sh
```

| Target | What it does |
| --- | --- |
| `test` | Runs `run.sh`. |
| `test-cleanup` | Runs `run.sh` with `E2E_CLEANUP=1`. |
| `clean` | Runs `run.sh` with `E2E_CLEANUP_ONLY=1`: removes what earlier runs left. |
| `deploy` | Runs `run.sh` with `E2E_DEPLOY_ONLY=1`: setup and the server checks, then prints the port-forward and `curl` commands. |
| `dry-run` | Runs `run.sh` with `E2E_DRY_RUN=1`: renders the chart with the phase 1 values and prints the commands; no cluster, no build. |
| `cluster` | Creates k3d cluster `gds-e2e` when it does not exist, without changing the default kubeconfig. `run.sh` does not delete a cluster created this way. |
| `images` | `bazel run //src/control-plane-services/global-discovery:image_load`, with `BAZEL_FLAGS`. |
| `all` | `images`, then `run.sh` with `E2E_SKIP_BUILD=1`. |
| `test-unit` | `bazel test //src/control-plane-services/global-discovery/...`. |
| `test-charts` | The chart's `lint` and `test` targets, then `tools/ci/check-helm-charts`. |
| `destroy` | Prints `k3d cluster delete gds-e2e` and runs it only with `CONFIRM=1`. |
| `check` | `bash -n`, and `shellcheck` when installed. |

## What run.sh does

1. Creates k3d cluster `gds-e2e` (one server, no agents, no load balancer,
   Traefik disabled) when it does not exist and `E2E_CREATE_CLUSTERS=1`,
   records it in the work directory, and waits for a Ready node. It reads
   the cluster's kubeconfig with `k3d kubeconfig get` into the work
   directory and passes `--kubeconfig` and `--context` on every `kubectl`
   and `helm` call. The default kubeconfig and its current context are never
   read or changed.
2. Builds the image with `bazel run
   //src/control-plane-services/global-discovery:image_load`, tags it
   `gds-e2e/gds-server:e2e-<image id>` so a rebuilt image rolls the pod, and
   imports it with `k3d image import`.
3. Installs the chart from this checkout as release `global-discovery` in
   namespace `global-discovery` with `manifests/gds-server-values.yaml`: the
   memory store, one replica, no PodDisruptionBudget, region `e2e-local`,
   `imagePullPolicy: Never`. `helm --wait` waits for readiness.
4. Port-forwards the REST port to `127.0.0.1:28080` and the admin port to
   `127.0.0.1:29091`, and runs the checks below.
5. With `E2E_CLEANUP=1`, deletes the cluster when `run.sh` created it, or
   otherwise uninstalls the release and deletes the namespace. Without it,
   everything stays installed and `run.sh` prints the commands to reach the
   server.

## Phase 1 checks

Clusterset `e2e`, region `e2e-local`. Each check is a `PASS` or `FAIL` line
in the summary; a failed required step skips the rest and prints the pods,
events and server logs.

| Area | Checks |
| --- | --- |
| Server | `/readyz` and `/healthz` are `ok`; `/metrics` has `nvcf_gds_build_info`; `GET /v1/serverinfo` reports the region; `GET /v1/clustersets` is empty. |
| Clusters | `PUT` registers `cluster-a` and `cluster-b` with ACTIVE leases; the clusterset lists 2 active clusters. |
| Pools | `POST` registers pool `blue` (REGISTERED); a heartbeat with the lease epoch turns it ACTIVE; `GET pools?model=llama-3&region=e2e-local` lists it; other models and regions list nothing. |
| MCS | `cluster-a` and `cluster-b` export `llm/frontend` (`manifests/frontend-export.json`, labelled as a pool frontend) with 2 and 1 ready endpoints; `GET imports/llm/frontend` merges both clusters without conflict; the labels derive pool `green` in each cluster. |
| Watch | `curl -N` on `pools:watch?model=llama-3` for 12 s sees `SNAPSHOT_START`, the pools, `SNAPSHOT_END`, then an `UPSERT` with `blue` DRAINING after an `UpdatePool` sent during the watch, and a `BOOKMARK` within 10 s. |
| Leases | A heartbeat with the epoch renews `cluster-a`; a stale epoch is 409 `LEASE_EPOCH_MISMATCH`; an unknown cluster is 404 `CLUSTER_NOT_REGISTERED`. `cluster-c` registers with a 10 s lease, exports one endpoint and sends no heartbeat: within 40 s its lease is EXPIRED, it stops participating in the import (endpoints hidden), its pool `green` is UNHEALTHY, and `ListClusters` omits it unless `includeExpired=true`. |
| Deregister | `DELETE clusters/cluster-b` removes its endpoints from the import, its pool from `ListPools` and the cluster itself. |

## Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `E2E_PHASES` | `1` | Phases to run, separated by spaces or commas. |
| `E2E_CLUSTER` | `gds-e2e` | k3d cluster of phase 1. Must be `gds-e2e` or start with `gds-e2e-`. |
| `E2E_CREATE_CLUSTERS` | `1` | Create missing clusters. `0` fails instead. |
| `E2E_CLEANUP` | `0` | Remove what the run created at the end, also after a failed step. |
| `E2E_CLEANUP_ONLY` | `0` | Only remove what earlier runs left, then exit. |
| `E2E_DEPLOY_ONLY` | `0` | Stop after the server checks and leave everything installed. |
| `E2E_DRY_RUN` | `0` | Render the chart and print the commands; no cluster, no build. |
| `E2E_SKIP_BUILD` | `0` | Reuse the image Bazel last loaded into Docker. |
| `E2E_TIMEOUT` | `60` | Seconds each polled check may take. |
| `E2E_HELM_TIMEOUT` | `5m` | `helm --wait` timeout. |
| `E2E_NAMESPACE` | `global-discovery` | Release namespace. |
| `E2E_REGION`, `E2E_CLUSTERSET` | `e2e-local`, `e2e` | Region of the server and the clusters, and the clusterset used. |
| `E2E_HTTP_LOCAL_PORT`, `E2E_ADMIN_LOCAL_PORT` | `28080`, `29091` | Local ports of the port-forward. |
| `E2E_IMAGE_REPOSITORY` | `gds-e2e/gds-server` | Repository the built image is tagged and imported as. |
| `E2E_WORK_DIR` | `$TMPDIR/global-discovery-k3d-e2e` | Kubeconfig files, the record of created clusters, logs and the watch capture. |
| `IMAGE_SOURCE` | `local` | Only `local` exists until an image is published. |
| `BAZEL`, `BAZEL_FLAGS` | `bazel`, empty | Bazel command and extra flags for the image build. |

## Adding a phase

Write `run_phase_<n>` in `run.sh` and add it to the `case` in `run_phases`.
Use `ensure_cluster <name> [k3d create args]` for each cluster (names start
with `gds-e2e-`, for example per-cluster `--k3s-arg --cluster-cidr=...` and
a shared `--network`), `build_image` once and `import_image <cluster>` per
cluster, `kc <cluster>` and `hm <cluster>` for every `kubectl` and `helm`
call, and `expect` or `once` for every assertion. Cleanup deletes every
cluster `ensure_cluster` created.

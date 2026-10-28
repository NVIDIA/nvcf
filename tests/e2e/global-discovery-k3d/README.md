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
(GDS) on local k3d clusters. `run.sh` builds the images from this checkout
with Bazel, installs the charts and checks each phase. The server and the MCS
controller are `src/control-plane-services/global-discovery`; the REST API is
described in that module's `docs/api.md`.

| Phase | Clusters | What runs |
| --- | --- | --- |
| 1 | hub | `gds-server` (chart `global-discovery`, memory store, one replica), driven over REST, pool agent registrations included |
| 2 | hub, a, b | `gds-mcs-controller` (chart `global-discovery-mcs-controller`, two replicas) in a and b against the hub's gds-server; export, import, pools and their label-derived agents, restart, unexport |
| 3 | hub, a, b | Stargate (chart `llm-request-router`, GDS export on) in a; a Pylon in b that finds it only through GDS, is the PYLON agent of its pool, registers and serves a request |

The hub is `$E2E_CLUSTER` (default `gds-e2e`); clusters a and b are
`$E2E_CLUSTER-a` and `$E2E_CLUSTER-b`. All three share the Docker network
`$E2E_CLUSTER-net`. a and b have distinct pod and service CIDRs
(`10.44.0.0/16` and `10.45.0.0/16`, `10.46.0.0/16` and `10.47.0.0/16`), and
`run.sh` adds static routes between their node containers so pods of one
cluster reach pod IPs of the other, as VPC peering does on EKS.

## Prerequisites

- Docker and `k3d` 5.x. `run.sh` creates the clusters and the network when
  they do not exist.
- `kubectl`, `helm` 3.x, `curl`, `jq` and `tar`.
- `bazel`, to build the images. Phase 3 builds the Stargate and Pylon images,
  which need `capnp` in `/usr/local/bin` or `/usr/bin` unless the Bazel
  cache already has them (see `docs/dev/inference-endpoints-quickstart.md`).
  `E2E_SKIP_BUILD=1` reuses the images of the last build.
- `busybox:1.37` (pulled when missing) for the phase 2 workload, and
  `openai-compatible-sample:e2e` for phase 3, built from
  `examples/function-samples/openai-compatible-sample` when missing.
- `bash` 3.2 or later. `run.sh` runs with the macOS default bash.
- Free local ports `28080`, `29091` (gds-server REST and admin) and `28000`
  (Stargate HTTP, phase 3).

Each k3s node needs inotify instances on the Docker host. When other k3d
clusters run, the host default of 128 (`fs.inotify.max_user_instances`) can
be used up; the new node then never registers and `run.sh` stops with a hint
about it. Raise the limit on the Docker host, for example
`colima ssh -- sudo sysctl -w fs.inotify.max_user_instances=512`, or stop
clusters you do not need.

## Run

From the repository root:

```bash
make -C tests/e2e/global-discovery-k3d test          # phases 1 2 3; leaves the clusters and releases in place
make -C tests/e2e/global-discovery-k3d test-cleanup  # removes what the run created
make -C tests/e2e/global-discovery-k3d test E2E_PHASES=1   # the single-cluster checks only
```

Or directly:

```bash
tests/e2e/global-discovery-k3d/run.sh
E2E_CLEANUP=1 E2E_PHASES="1 2" tests/e2e/global-discovery-k3d/run.sh
```

To run next to another run of this test, give it its own clusters, work
directory and ports:

```bash
E2E_CLUSTER=gds-e2e-mine E2E_WORK_DIR=/tmp/gds-e2e-mine \
  E2E_HTTP_LOCAL_PORT=38080 E2E_ADMIN_LOCAL_PORT=39091 E2E_ROUTER_LOCAL_PORT=38000 \
  E2E_CLEANUP=1 tests/e2e/global-discovery-k3d/run.sh
```

| Target | What it does |
| --- | --- |
| `test` | Runs `run.sh`. |
| `test-cleanup` | Runs `run.sh` with `E2E_CLEANUP=1`. |
| `clean` | Runs `run.sh` with `E2E_CLEANUP_ONLY=1`: removes what earlier runs left. |
| `deploy` | Runs `run.sh` with `E2E_DEPLOY_ONLY=1`: setup and the first checks of each phase, then prints the commands to reach gds-server. |
| `dry-run` | Runs `run.sh` with `E2E_DRY_RUN=1`: renders the charts and manifests of the phases and prints the commands; no cluster, no build. |
| `cluster` | Creates k3d cluster `gds-e2e` (the hub) when it does not exist, without changing the default kubeconfig. `run.sh` does not delete a cluster created this way. |
| `images` | Runs `run.sh` with `E2E_BUILD_ONLY=1`: builds the images of `E2E_PHASES` with Bazel (`BAZEL_FLAGS`) and loads them into Docker. |
| `all` | `images`, then `run.sh` with `E2E_SKIP_BUILD=1`. |
| `test-unit` | `bazel test //src/control-plane-services/global-discovery/...`. |
| `test-charts` | `lint` and `test` of the `global-discovery` and `global-discovery-mcs-controller` charts, then `tools/ci/check-helm-charts`. |
| `destroy` | Prints `k3d cluster delete` for the hub, a and b and runs it only with `CONFIRM=1`. |
| `check` | `bash -n`, and shellcheck from `PATH` or the `koalaman/shellcheck:v0.10.0` image when Docker has it. |

## What run.sh does

1. Creates the Docker network and each cluster it needs (one server, no
   agents, no load balancer, Traefik disabled) when they do not exist and
   `E2E_CREATE_CLUSTERS=1`, records them in the work directory, and waits for
   a Ready node. A reused cluster is connected to the network when needed. It
   reads each kubeconfig with `k3d kubeconfig get` into the work directory and
   passes `--kubeconfig` and `--context` on every `kubectl` and `helm` call.
   The default kubeconfig and its current context are never read or changed;
   `k3d cluster delete` gets a scratch `KUBECONFIG`, because it rewrites the
   file it loads.
2. Builds each image with `bazel build <target>.tar`, loads the tarball into
   Docker under `gds-e2e/<name>:e2e-<image id>` (also `:e2e-latest`) and
   imports it with `k3d image import`, checking that every node has it. It
   never uses the shared `<package>:latest` tag of the Bazel `_load` targets,
   which a concurrent build can change.
3. Installs `gds-server` in the hub as release `global-discovery` in
   namespace `global-discovery` with `manifests/gds-server-values.yaml` (memory
   store, one replica, a NodePort Service). A release left by an earlier run is
   restarted so every run starts from an empty store. The REST and admin
   ports are port-forwarded to `127.0.0.1:28080` and `127.0.0.1:29091`.
4. Phase 2: adds the static routes, then installs `gds-mcs-controller` in a
   and b (release `gds-mcs-controller`, namespace `gds-system`,
   `manifests/gds-mcs-controller-values.yaml`) with `gds.urls` set to the hub
   node's address on the network and the gRPC NodePort, clusterset `e2e-mcs`
   and cluster ids `a` and `b`. The workload is `manifests/mcs-demo.yaml` in a
   and `manifests/mcs-client.yaml` in b, in namespace `gds-e2e-demo`.
5. Phase 3: installs `llm-request-router` in a (namespace `gds-e2e-router`,
   `manifests/router-values.yaml`) and `manifests/pylon.yaml` (the Pylon
   Deployment and its labelled Service `pylon`) in b (namespace
   `gds-e2e-pylon`).
6. With `E2E_CLEANUP=1`, deletes the clusters and the network `run.sh`
   created; in clusters it reused it deletes the namespaces and releases (the
   workloads first, so ServiceExport finalizers can still deregister), removes
   the routes and disconnects the network. The CRDs of the controller chart
   stay in reused clusters (`helm.sh/resource-policy: keep`). Without it,
   everything stays installed and `run.sh` prints the commands to reach it.

## Phase 1 checks

Clusterset `e2e`, region `e2e-local`. Each check is a `PASS`, `FAIL` or
`SKIP` line in the summary; a failed required step skips the rest and prints
the pods, events and logs.

| Area | Checks |
| --- | --- |
| Server | `/readyz` and `/healthz` are `ok`; `/metrics` has `nvcf_gds_build_info`; `GET /v1/serverinfo` reports the region; `GET /v1/clustersets` has no clusterset `e2e` yet. |
| Clusters | `PUT` registers `cluster-a` and `cluster-b` with ACTIVE leases; the clusterset lists 2 active clusters. |
| Pools | `POST` registers pool `blue` (REGISTERED); a heartbeat with the lease epoch turns it ACTIVE; `GET pools?model=llama-3&region=e2e-local` lists it; other models and regions list nothing. |
| Pool agents | `POST pool-agents` registers KV_RELAY agent `relay-1` on `blue` (registration lease ACTIVE, blue's pool id; blue turns UNHEALTHY because a pool with agent registrations needs a live ingress agent and a relay is none); `GET pools/<id>` has one KV_RELAY agent with source API, 1 active registration and `kvRelayLive`; a heartbeat with a stale epoch is 409 `LEASE_EPOCH_MISMATCH`, one with the epoch renews; `relay-2` adds a second registration (2 active) and `relay-1` still heartbeats (no fencing); `DELETE ...agents/FRONTEND_ROLE_KV_RELAY?agent_id=relay-1` leaves 1; without registrations blue is ACTIVE again. A pool registered only through a KV_RELAY agent (`relay-only`) is REGISTERED with `kvRelayLive`; one registered only through a PYLON agent (`pylon-only`) is ACTIVE with `readyIngressCount` 0 and a live agent; both are gone after their registrations are deregistered. |
| MCS | `cluster-a` and `cluster-b` export `llm/frontend` (`manifests/frontend-export.json`, labelled as a pool frontend) with 2 and 1 ready endpoints; `GET imports/llm/frontend` merges both clusters without conflict; the labels derive pool `green` in each cluster. |
| Watch | `curl -N` on `pools:watch?model=llama-3` for 12 s sees `SNAPSHOT_START`, the pools, `SNAPSHOT_END`, then an `UPSERT` with `blue` DRAINING after an `UpdatePool` sent during the watch, and a `BOOKMARK` within 10 s. |
| Leases | A heartbeat with the epoch renews `cluster-a`; a stale epoch is 409 `LEASE_EPOCH_MISMATCH`; an unknown cluster is 404 `CLUSTER_NOT_REGISTERED`. `cluster-c` registers with a 10 s lease, exports one endpoint and sends no heartbeat: within 40 s its lease is EXPIRED, it stops participating in the import (endpoints hidden), its pool `green` is UNHEALTHY, and `ListClusters` omits it unless `includeExpired=true`. |
| Deregister | `DELETE clusters/cluster-b` removes its endpoints from the import, its pool from `ListPools` and the cluster itself. |

## Phase 2 checks

Clusterset `e2e-mcs`, cluster ids `a` and `b`.

| Area | Checks |
| --- | --- |
| Setup | Clusters a and b are Ready with their CIDRs; static routes between their pod CIDRs (or a `SKIP` with the reason, see below); the hub's gRPC NodePort address; `helm --wait` returns with both controller replicas ready in each cluster (connected to GDS, the leader registered). |
| Controllers | GDS lists clusters `a` and `b` with ACTIVE leases; the Lease `gds-mcs-controller.gds.nvidia.com` in each cluster is held by a running controller pod. |
| Export and import | Labelling Service `echo` (2 ready replicas) in a with `gds.nvidia.com/export=true` creates a managed ServiceExport with `Valid=True` and `Ready=True`; the GDS import has 2 ready endpoints from `a`; a and b get a ClusterSetIP ServiceImport whose `spec.ips` is its derived Service's ClusterIP; the EndpointSlices in b labelled `multicluster.kubernetes.io/source-cluster=a` hold exactly the 2 ready echo pod IPs of a; `wget` from a pod in b to the derived ClusterIP answers with an echo pod name from a. |
| Pool agents from labels | Service `echo-pylon` (labels `gds.nvidia.com/export=true`, `gds.nvidia.com/role=pylon`, `gds.nvidia.com/pool=demo`, over the 2 echo pods) is exported Valid and Ready and joins pool `a/demo`: its `agents[]` are OPENAI (`frontend`) and PYLON (`echo-pylon`), both with source LABEL and 2 ready endpoints, `readyEndpointCount` stays 2 (OPENAI only) and `readyIngressCount` is 4; the pool still names `frontend` as its OPENAI frontend. Service `echo-combined` (annotation `gds.nvidia.com/roles: pool-frontend,pylon`, pool `combo`) yields pool `a/combo` with an OPENAI and a PYLON agent on the same Service, its 2 ready endpoints counted once in `readyIngressCount`. |
| Scale down | Scaling echo to 1 leaves 1 ready endpoint in b's slices and in the GDS import. |
| Labelled pool | Service `frontend` (labels `gds.nvidia.com/export=true`, `gds.nvidia.com/role=pool-frontend`, `gds.nvidia.com/pool=demo`) appears as pool `a/demo`, ACTIVE, with `frontend` as its OPENAI frontend, in `GET /v1/clustersets/e2e-mcs/pools`; `UpdatePool` with `desiredState` DRAINING (the overlay) turns it DRAINING; its ServiceExport in a and ServiceImport in b stay. |
| API-registered pool | `RegisterPool api` with `services[] = gds-e2e-demo/api-svc` makes the controller in a create ServiceExport `api-svc` labelled `gds.nvidia.com/pool-id=<id>`; b imports it; `DeregisterPool` removes the export and the import. |
| Controller restart | After `rollout restart` of both controllers, the ServiceImport `echo` in b keeps its UID and ClusterIP, its slices keep the ready endpoint, the ServiceExport in a stays Valid and Ready, a new pod holds the Lease, cluster `a`'s GDS lease is ACTIVE, and the derived ClusterIP still answers. |
| Unexport | Removing the label deletes ServiceExport `echo` in a, the import in GDS (404), and in b the ServiceImport, the derived Service and the slices. |

## Phase 3 checks

| Area | Checks |
| --- | --- |
| Router export | Stargate (one replica, open worker auth, `globalDiscovery.enabled` and `export.enabled`, dial scheme `http`) is ready in a; the controller exports its Service (`gds.nvidia.com/role=router`) with `Valid` and `Ready`; `GET /v1/clustersets/e2e-mcs/routers` lists the router of cluster `a` with dial scheme `http`, gRPC port 50071, QUIC port 50072 (the chart's `gds.nvidia.com/quic-port-name: quic`), `ports[]` with `grpc` and `quic` (UDP), and the Stargate pod IP as a ready replica. |
| Pylon | A plain Pylon Deployment in b with `--gds-url`, `--gds-clusterset`, `--cluster-id=b`, `--gds-pool-name=e2e-pylon` and no `--stargate-address`, next to the OpenAI-compatible sample backend, logs `GDS Stargate seeds changed`. GDS lists pool `b/e2e-pylon` ACTIVE with one PYLON agent of source BOTH: the Service `pylon` over the Pylon pod (labels `export=true`, `role=pylon`, `pool=e2e-pylon`, port `metrics` 9089, as the Pylon Operator renders with `--gds-pool-export`) with 1 ready endpoint, and the Pylon's own registration under its `--inference-server-id` (1 active) with attribute `models: test-model`. These checks need no routes between the pod CIDRs. |
| Registration | `pylon_registration_stream_connected` and `pylon_reverse_tunnel_connected` are 1 for the Stargate pod of a; Stargate's `/v1/models` lists `test-model` with a registered and healthy server of cluster `b`; a streamed chat completion sent to Stargate in a (with the `x-request-id`, `x-model` and `x-input-tokens` headers the LLM gateway sets) is answered by the sample backend in b over the reverse tunnel (`x-stargate-cluster-id: b`). |

Registration dials the Stargate pod IP that GDS lists as the seed. The
reverse tunnel dials the hostname Stargate advertises, which a deployment
serves from DNS or a load balancer that every cluster reaches (design 4.5).
Phase 3 sets `kubernetes.advertisedHostnameTemplate` to
`llm-router.a.gds-e2e.test` (`E2E_ROUTER_HOST`) and maps it to the Stargate
pod IP with a `hostAliases` entry in the Pylon pod. Pylon runs with
`--quic-insecure` against Stargate's self-signed QUIC identity; worker auth is
open (`auth.allowOpen`). TLS and credentials are therefore not covered.

Without routes between the pod CIDRs (`E2E_CROSS_CLUSTER_ROUTES=0`, or when
the routes cannot be added or the clusters share pod CIDRs) the setup records
a `SKIP` with the reason, the control plane objects are still asserted, and
the data path checks (`wget` through the derived ClusterIP, and the phase 3
registration checks) are recorded as `SKIP`.

## Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `E2E_PHASES` | `1 2 3` | Phases to run, separated by spaces or commas. |
| `E2E_CLUSTER` | `gds-e2e` | The hub cluster. Must be `gds-e2e` or start with `gds-e2e-`. |
| `E2E_CLUSTER_A`, `E2E_CLUSTER_B` | `$E2E_CLUSTER-a`, `$E2E_CLUSTER-b` | The member clusters of phases 2 and 3. |
| `E2E_NETWORK` | `$E2E_CLUSTER-net` | The shared Docker network. Must start with `gds-e2e-`. |
| `E2E_CREATE_CLUSTERS` | `1` | Create missing clusters and the network. `0` fails instead. |
| `E2E_CROSS_CLUSTER_ROUTES` | `1` | Add static routes between the pod CIDRs of a and b. |
| `E2E_POD_CIDR_A`, `E2E_SERVICE_CIDR_A`, `E2E_POD_CIDR_B`, `E2E_SERVICE_CIDR_B` | `10.44.0.0/16`, `10.45.0.0/16`, `10.46.0.0/16`, `10.47.0.0/16` | k3s `--cluster-cidr` and `--service-cidr` of new member clusters. |
| `E2E_CLEANUP` | `0` | Remove what the run created at the end, also after a failed step. |
| `E2E_CLEANUP_ONLY` | `0` | Only remove what earlier runs left, then exit. |
| `E2E_DEPLOY_ONLY` | `0` | Stop after the setup checks of each phase and leave everything installed. |
| `E2E_DRY_RUN` | `0` | Render the charts and manifests and print the commands; no cluster, no build. |
| `E2E_BUILD_ONLY` | `0` | Build and load the images of `E2E_PHASES`, then exit. |
| `E2E_SKIP_BUILD` | `0` | Reuse the images of the last build (`gds-e2e/<name>:e2e-latest`). |
| `E2E_TIMEOUT` | `60` | Seconds each polled check may take. |
| `E2E_HELM_TIMEOUT` | `5m` | `helm --wait` timeout. |
| `E2E_NAMESPACE` | `global-discovery` | gds-server namespace in the hub. |
| `E2E_CONTROLLER_NAMESPACE` | `gds-system` | Controller namespace in a and b. |
| `E2E_DEMO_NAMESPACE`, `E2E_ROUTER_NAMESPACE`, `E2E_PYLON_NAMESPACE` | `gds-e2e-demo`, `gds-e2e-router`, `gds-e2e-pylon` | Workload namespaces of phases 2 and 3. |
| `E2E_REGION`, `E2E_CLUSTERSET` | `e2e-local`, `e2e` | Region and the clusterset of phase 1. |
| `E2E_MCS_CLUSTERSET` | `e2e-mcs` | Clusterset of phases 2 and 3. |
| `E2E_CLUSTER_ID_A`, `E2E_CLUSTER_ID_B` | `a`, `b` | Cluster ids the controllers register. |
| `E2E_HTTP_LOCAL_PORT`, `E2E_ADMIN_LOCAL_PORT` | `28080`, `29091` | Local ports of the gds-server port-forward. |
| `E2E_ROUTER_LOCAL_PORT` | `28000` | Local port of the Stargate HTTP port-forward (phase 3). |
| `E2E_ROUTER_HOST` | `llm-router.a.gds-e2e.test` | Hostname Stargate advertises for the reverse tunnel. |
| `E2E_IMAGE_REPOSITORY`, `E2E_CONTROLLER_IMAGE_REPOSITORY`, `E2E_STARGATE_IMAGE_REPOSITORY`, `E2E_PYLON_IMAGE_REPOSITORY` | `gds-e2e/gds-server`, `gds-e2e/gds-mcs-controller`, `gds-e2e/stargate`, `gds-e2e/pylon` | Repositories the built images are loaded as. |
| `E2E_BUSYBOX_IMAGE`, `E2E_SAMPLE_IMAGE` | `busybox:1.37`, `openai-compatible-sample:e2e` | Workload images. |
| `E2E_WORK_DIR` | `$TMPDIR/global-discovery-k3d-e2e` | Kubeconfig files, records of created clusters, networks and routes, logs. |
| `IMAGE_SOURCE` | `local` | Only `local` exists until images are published. |
| `BAZEL`, `BAZEL_FLAGS` | `bazel`, empty | Bazel command and extra flags for the image builds. |

## Adding a phase

Write `run_phase_<n>` in `run.sh`, add it to the `case` in `run_phases` and
`check_phases`, and list its images in `build_images`. Use `setup_hub` and
`setup_members` for the shared setup, `ensure_cluster <name> [k3d create
args]` for any other cluster (names start with `gds-e2e-`), `import_image
<cluster> <image>...` for images the charts run with `imagePullPolicy:
Never`, `kc <cluster>` and `hm <cluster>` for every `kubectl` and `helm`
call, and `expect` or `once` for every assertion. Cleanup deletes every
cluster `ensure_cluster` created; add namespaces a phase creates in reused
clusters to `cleanup_member`.

## Verification record

Runs on macOS with colima (Docker with the containerd image store), k3d 5.9.0
and k3s v1.35.5, with `/bin/bash` 3.2:

- Phases 1, 2 and 3 from scratch with the Bazel builds and `E2E_CLEANUP=1`:
  100 checks passed, then the three clusters and the network were deleted.
- The same with reused clusters and `E2E_SKIP_BUILD=1`: 101 passed (the extra
  check is the restart of the reused gds-server).
- `E2E_DEPLOY_ONLY=1`: 28 passed. `E2E_CLEANUP_ONLY=1` removed created
  clusters, and in clusters treated as reused it deleted the workload
  namespaces (ServiceExport finalizers resolved by the running controller),
  the releases and the routes.
- `make check` (shellcheck 0.10.0) and `make dry-run`.

Not covered: TLS between the clients and GDS, between Pylon and Stargate,
and worker credentials; DynamoDB (the hub uses the memory store); more than
one node per cluster; the CoreDNS multicluster plugin.

<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Global Discovery Service

The Global Discovery Service (GDS) is the multi-region discovery backend of
the Dynamo Global Router. It registers member clusters, implements the
Multi-Cluster Services API (KEP-1645) server side, and keeps the pool and
router registries that Global Router replicas and pylons read. The design is
`docs-gds/global-discovery-service-design.md` at the repository root; the
step list is `docs-gds/implementation-plan.md`.

Module: `github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery`.

## Status

- The `nvidia.gds.v1` proto contract with generated Go, grpc-gateway and
  OpenAPI code.
- The well-known label, annotation, error reason and limit constants, and the
  derived Service name of an import.
- The store contract, the in-memory store and the store conformance suite.
- The DynamoDB store (one table, replicated as a Global Table) and
  `gds-admin create-table`. `docs/storage.md`, `docs/iam.md` and
  `docs/consistency-dynamodb.md` describe the table, its IAM policies and its
  cross-region behaviour.
- Lease math, writer generations and pool state derivation.
- The Go client `pkg/client` (failover, typed calls, watch loop, pool and
  router caches, metrics) and the in-memory test server `pkg/fakegds`.

- `gds-server` serves every service over gRPC and REST with the memory or
  DynamoDB store: cluster leases, ServiceExports and endpoint sets, merged
  ServiceImports with conflict resolution, pools derived from labelled
  exports with the API overlay, routers, and watches with resume.
- `gds-admin` inspects a store and deletes clusters.
- `gds-mcs-controller` implements the Multi-Cluster Services API in every
  member cluster on top of GDS: label-driven and explicit ServiceExports,
  pool exports, ServiceImports with derived Services and EndpointSlices,
  and the cluster lease. See [gds-mcs-controller](#gds-mcs-controller) and
  `docs/mcs-controller.md`.
- Packaging: the `nvcf-global-discovery` and gds-admin OCI images, the
  `deploy/helm/global-discovery` chart, `docs/networking.md`, and phase 1 of
  the k3d end-to-end test in `tests/e2e/global-discovery-k3d`.
- Not yet: the MCS controller chart and the multi-cluster end-to-end
  phases.

## Layout

- `api/proto/nvidia/gds/v1/`: the proto contract (`common`, `cluster`, `mcs`,
  `pool`, `server`). `api/proto/google/api/` is vendored from
  `buf.build/googleapis/googleapis`.
- `api/gen/go/nvidia/gds/v1/`: generated Go (package `gdsv1`). Do not edit.
- `api/gen/openapi/gds.swagger.json`: generated OpenAPI v2. Do not edit.
- `api/`: package `api` with `labels.go`, `errors.go` (error reasons,
  condition reasons, limits) and `derived.go` (`DerivedServiceName`).
- `internal/store/`: `Store` and its per-aggregate interfaces, record types
  and typed errors.
- `internal/store/memory/`: in-memory `Store`.
- `internal/store/dynamodb/`: DynamoDB `Store` (`store.go`), item keys
  (`keys.go`), record codec (`codec.go`), table definition and creation
  (`schema.go`), client construction (`client.go`).
- `internal/store/dynamodb/dynamodblocal/`: starts `amazon/dynamodb-local`
  for integration tests.
- `internal/store/storetest/`: conformance suite every `Store` must pass.
- `internal/registry/`: lease evaluation, TTL bounds, generations, roles and
  pool state derivation.

- `cmd/gds-server/`: the server entrypoint.
- `cmd/gds-admin/`: the admin CLI (`describe`, `delete-cluster`).
- `internal/config/`: `gds-server` flags with `GDS_` environment fallbacks.
- `internal/server/`: listeners, interceptors, health, readiness, graceful
  shutdown, tracing setup and `NewApp`, which wires every component.
- `internal/api/`: the gRPC service implementations, validation and error
  mapping.
- `internal/auth/`: the `Authenticator` and `Principal` seam with
  `NoopAuthenticator` and interceptors.
- `internal/view/`: the materialised view per clusterset (derivation of
  clusters, ServiceImports, Pools and Routers, indexes), the refresher, the
  watch hub and compaction.
- `internal/registry/`: lease evaluation, TTL bounds, generations, pool ids,
  DNS label checks, roles and pool state derivation.
- `internal/registry/merge/`: ServiceImport merge and conflict resolution
  ported from Cilium.
- `internal/store/`: `Store` and its per-aggregate interfaces, record types
  and typed errors; `memory/` (in-memory store), `storetest/` (conformance
  suite), `instrumented/` (metrics and spans around any store).
- `internal/storefactory/`: opens the store `--store` selects; the seam for
  the DynamoDB store.
- `internal/metrics/`: the `nvcf_gds_*` collectors.
- `internal/convert/`: store records to and from proto messages.
- `internal/clock/`: real and fake clocks.
- `internal/version/`: build metadata set by Bazel `x_defs`.
- `cmd/gds-server/`: server entrypoint (configuration only for now).
- `pkg/client/`: the Go client library; usage, failover and cache semantics
  are in `pkg/client/README.md`.
- `pkg/fakegds/`: in-memory implementation of the five GDS services for tests
  of clients, the controller and integrations.
- `cmd/gds-mcs-controller/`: the MCS controller entrypoint.
- `internal/mcs/`: the MCS controller: `config` (flags, scheme, manager
  options), `identity` (clusterset and cluster ids, ClusterProperty),
  `lease` (cluster registration and heartbeats), `export` (ServiceExport
  reconciler, resync, label-driven and pool auto-export), `importer`
  (WatchServiceImports and the import controller), `convert` (Kubernetes
  to GDS mappings), `metrics` (`nvcf_gds_mcs_controller_*`), `tracing`,
  `app` (wiring), `mcstest` (test helpers) and `integration` (envtest
  suite).
- `config/crd/`: the CRDs the controller needs, embedded: ServiceExport and
  ServiceImport from sigs.k8s.io/mcs-api and the ClusterProperty CRD in
  `config/crd/bases/`.
- `config/rbac/role.yaml`: the controller ClusterRole, generated by
  `hack/codegen.sh` from the `+kubebuilder:rbac` markers.
- `docs/mcs-controller.md`: controller behaviour, labels, conditions,
  garbage collection, flags, RBAC, metrics and the optional CoreDNS
  multicluster plugin.

- `cmd/gds-admin/`: admin CLI; `create-table` is in `cmd/gds-admin/createtable`.
- `docs/api.md`: REST paths, JSON and stream shapes, errors and limits.
- `docs/storage.md`: DynamoDB item catalogue, conditional writes, table
  definition, Global Table replicas, TTL, PITR, capacity and limits.
- `docs/iam.md`: server and admin IAM policies, IRSA trust policy.
- `docs/consistency-dynamodb.md`: cross-region last-writer-wins reasoning.

- `docs/api.md`: REST paths, JSON and stream shapes, errors and limits.
- `docs/consistency.md`: write-through, refresh, leases and revisions.
- `docs/networking.md`: the regional internal NLB, Route53 names, health
  checks, TTLs and client failover.
- `hack/proto.sh`: proto lint and code generation.

## Run locally

```bash
GOWORK=off go run ./cmd/gds-server --region=local --log-level=debug
```

The server listens on `:9090` (gRPC), `:8080` (REST) and `:9091` (admin).
Register a cluster, export a labelled pool frontend, and list the derived
pool:

```bash
curl -sX PUT localhost:8080/v1/clustersets/dev/clusters/a -d '{"region":"local"}'
curl -sX PUT localhost:8080/v1/clustersets/dev/clusters/a/exports/llm/frontend -d '{
  "spec": {"type": "SERVICE_TYPE_CLUSTER_SET_IP",
    "ports": [{"name": "http", "port": 8000}],
    "labels": {"gds.nvidia.com/role": "pool-frontend", "gds.nvidia.com/pool": "demo"},
    "annotations": {"gds.nvidia.com/pool-models": "llama-3"}}}'
curl -sX PUT localhost:8080/v1/clustersets/dev/clusters/a/exports/llm/frontend/endpoints -d '{
  "endpoints": [{"addresses": ["10.0.0.5"], "conditions": {"ready": true}}]}'
curl -s 'localhost:8080/v1/clustersets/dev/pools?model=llama-3'
curl -sN localhost:8080/v1/clustersets/dev/pools:watch
curl -s localhost:9091/readyz
```

A generation of 0 (or none) in a write asks the server to assign one, which
is convenient with curl; controllers and clients send their own.

## gds-server flags

Every flag has a `GDS_` environment fallback named after it, for example
`GDS_REFRESH_INTERVAL`; a flag on the command line wins.

| Flag | Default | Meaning |
| :---- | :---- | :---- |
| `--grpc-listen-address` | `:9090` | gRPC listener (h2c, or TLS) |
| `--http-listen-address` | `:8080` | REST gateway listener |
| `--admin-listen-address` | `:9091` | `/metrics`, `/healthz`, `/readyz`, `/debug/pprof` |
| `--region` | `AWS_REGION` | Region reported by `GetServerInfo` |
| `--store` | `memory` | `memory` or `dynamodb` |
| `--dynamodb-table`, `--dynamodb-endpoint` | `gds`, none | DynamoDB table and endpoint override |
| `--refresh-interval` | `2s` | Full reload of each clusterset |
| `--lease-eval-interval` | `1s` | Lease evaluation without a store read |
| `--clusterset-discovery-interval` | `30s` | Listing of the store's clustersets |
| `--lease-skew` | `2s` | Clock skew allowance before a lease expires |
| `--retention` | `24h` | How long records of a dead cluster or pool are kept |
| `--compaction-interval` | `10m` | Interval of the retention cleanup |
| `--bookmark-interval` | `5s` | Watch bookmarks; also how long a GONE pool stays visible |
| `--watch-history` | `10000` | Events kept per clusterset for resume |
| `--watch-queue-size` | `1024` | Per-watcher queue; an overflowing watcher is re-snapshotted |
| `--max-watchers` | `5000` | Concurrent watch streams per replica |
| `--max-message-bytes` | `4194304` | gRPC message size limit |
| `--advertise-endpoints` | none | JSON list returned by `GetServerInfo` |
| `--tls-cert-file`, `--tls-key-file` | none | TLS on the gRPC and REST listeners |
| `--shutdown-drain` | `5s` | Time reported not ready before the replica stops serving |
| `--shutdown-timeout` | `25s` | Graceful stop of the gRPC and REST servers |
| `--otel-enabled` | `false` | OTLP tracing configured by the `OTEL_*` environment |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error` |

The gRPC server pings idle connections after 60 s (timeout 20 s), inside the
350 s NLB idle timeout, and accepts client pings every 10 s or more, also
without active streams.

## Operations

- Readiness (`/readyz` and the gRPC health service, for `""` and every
  service name) requires a store ping, one completed refresh of every
  clusterset the store knows and a view younger than three refresh
  intervals.
- Shutdown on SIGTERM: not ready and NOT_SERVING, drain for
  `--shutdown-drain`, end watches with `UNAVAILABLE SHUTTING_DOWN`, stop REST
  and gRPC within `--shutdown-timeout`, flush traces. Set the pod
  `terminationGracePeriodSeconds` above drain plus timeout (40 s).
- Metrics are `nvcf_gds_*`: clusters, exports, endpoints, service imports,
  pools and routers gauges per clusterset; lease expirations; watchers,
  watch events sent, watch resyncs and rejected watches; view refresh
  duration, failures and age; store request duration and conditional
  failures; compaction deletions; build info; and the gRPC server metrics as
  `nvcf_gds_grpc_server_*`. Counters are pre-initialised.
- Logs are zap JSON. Every call logs `finished call` with the method, code,
  duration, peer and the request's clusterset, cluster, namespace, name,
  pool id and `x-request-id`: debug for success, info for client errors,
  warn for unavailability, error otherwise.
- Traces: one otelgrpc server span per call with `gds.clusterset` and
  `nvcf.cluster.id`, a client span per store call (`gds.store.<op>`) and a
  span per refresh (`gds.view.refresh`) and compaction pass. The REST
  gateway forwards `traceparent`, `tracestate`, `baggage` and
  `x-request-id` to the gRPC call.

## Deployment

The chart is `deploy/helm/global-discovery/global-discovery`; its README
lists every value. Per GDS region:

1. Create the DynamoDB table once and add a replica in every GDS region
   (`gds-admin create-table`, `docs/storage.md`).
2. Create the server IAM role with the policy and IRSA trust policy of
   `docs/iam.md`.
3. Install the chart in the region's GDS cluster with the image, the
   region, the role and the internal NLB:

   ```yaml
   image:
     repository: <registry>/nvcf-global-discovery
   region: us-east-1
   serviceAccount:
     irsaRoleArn: arn:aws:iam::<account>:role/gds-server
   externalService:
     enabled: true
   advertiseEndpoints:
     - {region: us-east-1, grpcUrl: "http://gds.us-east-1.prod.internal:9090"}
     - {region: us-west-2, grpcUrl: "http://gds.us-west-2.prod.internal:9090"}
   ```

4. Point the Route53 records at the region's NLB: the per-region name and
   the region's entry of the global latency name (`docs/networking.md`).
5. Configure clients with the global name first (`docs/networking.md`,
   Clients).

The chart runs three replicas spread over zones with a zero-unavailable
rolling update, readiness on `/readyz` and liveness on `/healthz` of the
admin port, a 40 s termination grace period, a PodDisruptionBudget and
optional HPA and ServiceMonitor. With `store.kind: memory` it allows one
replica only; `tests/e2e/global-discovery-k3d` installs it that way on k3d.

## gds-admin

```bash
gds-admin --store=dynamodb --dynamodb-table=gds describe
gds-admin describe --clusterset=prod --output=json
gds-admin delete-cluster --clusterset=prod --cluster=use1-a --yes
```

Store flags (`--store`, `--dynamodb-table`, `--dynamodb-endpoint`,
`--region`) and their `GDS_` fallbacks match `gds-server`. `delete-cluster`
prints what it would delete and exits with status 2 without `--yes`.
`create-table` arrives with the DynamoDB store.

## gds-mcs-controller

The MCS controller runs in every member cluster (chart
`deploy/helm/global-discovery-mcs-controller`). It needs the CRDs of
`config/crd` and the ClusterRole of `config/rbac/role.yaml`, plus a Role for
leader election Leases in its namespace. `docs/mcs-controller.md` describes
its behaviour, labels, conditions, garbage collection, pending namespaces,
the reachability prerequisite, the optional CoreDNS multicluster plugin,
every flag, RBAC and metrics.

Run it out of cluster against the current kubeconfig and a local
`gds-server`:

```bash
kubectl apply -f "$(GOWORK=off go list -m -f '{{.Dir}}' sigs.k8s.io/mcs-api)/config/crd"
kubectl apply -f config/crd/bases
GOWORK=off go run ./cmd/gds-mcs-controller --gds-url=http://localhost:9090 \
  --gds-allow-insecure-http --clusterset-id=dev --cluster-id=a --region=local \
  --leader-elect=false --metrics-bind-address=:18080 --health-probe-bind-address=:18081
```

Main flags: `--gds-url` (repeatable, or `GDS_URLS`), `--gds-tls-ca-file`,
`--gds-allow-insecure-http`, `--clusterset-id`, `--cluster-id`,
`--cluster-identity-source` (`auto`, `flags`, `clusterproperty`),
`--region`, `--cluster-labels`, `--lease-ttl` (30 s), `--watch-namespaces`,
`--create-namespaces`, `--adopt-unmanaged-imports`, `--auto-export-pools`
(true), `--write-cluster-properties`, `--deregister-on-shutdown`,
`--endpoints-per-slice` (100), `--max-concurrent-reconciles` (4),
`--metrics-bind-address`, `--health-probe-bind-address`, `--leader-elect`
(true), `--operator-namespace` (`POD_NAMESPACE`) and `--otel-enabled`.

Tests: the unit tests use the controller-runtime fake client and
`pkg/fakegds` and are part of `make test`. The envtest suite in
`internal/mcs/integration` runs the whole controller against kube-apiserver
and etcd (`make envtest-assets`, then `make test-envtest`); it skips
without `KUBEBUILDER_ASSETS`.

## Make targets

Run from this directory. The Makefile sets `GOWORK=off`.

```bash
make build         # go build ./... and _output/bin/{gds-server,gds-admin,gds-mcs-controller}
make test          # unit tests with -race; fails below 85 percent coverage of internal/
make envtest-assets  # kube-apiserver and etcd for envtest into _output/envtest
make test-envtest  # gds-mcs-controller envtest suite against a real API server
make codegen-update  # regenerate config/rbac/role.yaml from the RBAC markers
make codegen-check   # fail when config/rbac is stale
make test-integration  # DynamoDB store and gds-admin against dynamodb-local (Docker)
make vet           # go vet and gofmt
make lint          # golangci-lint with .golangci.yml
make proto-tools   # install the pinned buf and protoc plugins into _output/tools
make proto         # buf lint, then regenerate api/gen
make proto-check   # buf lint, then fail when api/gen is stale
make proto-breaking AGAINST=origin/main   # buf breaking against a git ref
```

`make test-integration` starts `amazon/dynamodb-local` through
testcontainers-go and skips when Docker is unreachable. It takes
`DOCKER_HOST` from the current docker context (colima, Docker Desktop);
`GDS_TEST_DYNAMODB_ENDPOINT=http://localhost:8000` uses a running endpoint
instead.

`make proto` needs buf and the protoc plugins in `$HOME/go/bin`; the pinned
versions are listed in `hack/proto.sh`. `go generate ./api` runs the same
script.

## Bazel

From the repository root:

```bash
bazel build //src/control-plane-services/global-discovery/...
bazel test //src/control-plane-services/global-discovery/... --test_output=errors
bazel run //src/control-plane-services/global-discovery/cmd/gds-server -- --help
bazel run //src/control-plane-services/global-discovery/cmd/gds-admin -- create-table -h
bazel run //src/control-plane-services/global-discovery/cmd/gds-mcs-controller -- --help
bazel build //src/control-plane-services/global-discovery:controller_image
KUBEBUILDER_ASSETS=$PWD/src/control-plane-services/global-discovery/_output/envtest/k8s/1.34.1-darwin-arm64 \
  bazel test //src/control-plane-services/global-discovery/internal/mcs/integration:integration_test
bazel run //:gazelle -- src/control-plane-services/global-discovery
bazel mod tidy     # after adding an external import
```

Images (multi-arch indexes for the release pipeline, host-arch images for
local use):

```bash
bazel build //src/control-plane-services/global-discovery:image_index        # gds-server
bazel build //src/control-plane-services/global-discovery:admin_image_index  # gds-admin
bazel run //src/control-plane-services/global-discovery:image_load           # into Docker
```

Both load targets tag `src/control-plane-services/global-discovery:latest`;
retag the first before loading the second. The binaries are at
`/usr/bin/gds-server` and `/usr/bin/gds-admin`, which
`image_entrypoint_mode_test` and `admin_image_entrypoint_mode_test` check
are executable.

The DynamoDB integration targets (`dynamodb_integration_test`,
`createtable_integration_test`) are tagged `external` and `requires-docker`
and run unsandboxed. Pass the Docker endpoint through, for example
`DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')`,
or skip them with `--test_tag_filters=-requires-docker`.

The generated Go package is a plain `go_library`
(`//src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1:gds`);
`gazelle:proto disable_global` keeps Gazelle from generating proto rules.
`//src/control-plane-services/global-discovery/api/proto:proto_srcs` exports
the proto sources for vendored copies. Gazelle does not write the license
header into new BUILD files; add it by hand.

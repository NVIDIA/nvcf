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
- `gds-server` serves every service over gRPC and REST with the in-memory
  store: cluster leases, ServiceExports and endpoint sets, merged
  ServiceImports with conflict resolution, pools derived from labelled
  exports with the API overlay, routers, and watches with resume.
- `gds-admin` inspects a store and deletes clusters.
- Not yet: the DynamoDB store (`--store=dynamodb` fails with a clear error
  until it is built in), the Go client, the MCS controller and the Rust
  crate.

## Layout

- `api/proto/nvidia/gds/v1/`: the proto contract (`common`, `cluster`, `mcs`,
  `pool`, `server`). `api/proto/google/api/` is vendored from
  `buf.build/googleapis/googleapis`.
- `api/gen/go/nvidia/gds/v1/`: generated Go (package `gdsv1`). Do not edit.
- `api/gen/openapi/gds.swagger.json`: generated OpenAPI v2. Do not edit.
- `api/`: package `api` with `labels.go`, `errors.go` (error reasons,
  condition reasons, limits) and `derived.go` (`DerivedServiceName`).
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
- `docs/api.md`: REST paths, JSON and stream shapes, errors and limits.
- `docs/consistency.md`: write-through, refresh, leases and revisions.
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

## Make targets

Run from this directory. The Makefile sets `GOWORK=off`.

```bash
make build         # go build ./... and _output/bin/{gds-server,gds-admin}
make test          # unit tests with -race; fails below 85 percent coverage of internal/
make vet           # go vet and gofmt
make lint          # golangci-lint with .golangci.yml
make proto         # buf lint, then regenerate api/gen
make proto-check   # buf lint, then fail when api/gen is stale
make proto-breaking AGAINST=origin/main   # buf breaking against a git ref
```

`make proto` needs buf and the protoc plugins in `$HOME/go/bin`; the pinned
versions are listed in `hack/proto.sh`. `go generate ./api` runs the same
script.

## Bazel

From the repository root:

```bash
bazel build //src/control-plane-services/global-discovery/...
bazel test //src/control-plane-services/global-discovery/... --test_output=errors
bazel run //src/control-plane-services/global-discovery/cmd/gds-server -- --help
bazel run //:gazelle -- src/control-plane-services/global-discovery
bazel mod tidy     # after adding an external import
```

The generated Go package is a plain `go_library`
(`//src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1:gds`);
`gazelle:proto disable_global` keeps Gazelle from generating proto rules.
`//src/control-plane-services/global-discovery/api/proto:proto_srcs` exports
the proto sources for vendored copies. Gazelle does not write the license
header into new BUILD files; add it by hand.

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

This module holds the foundation the server, the MCS controller and the
clients build on:

- The `nvidia.gds.v1` proto contract with generated Go, grpc-gateway and
  OpenAPI code.
- The well-known label, annotation, error reason and limit constants, and the
  derived Service name of an import.
- The store contract, the in-memory store and the store conformance suite.
- Lease math, writer generations and pool state derivation.
- The `gds-server` configuration. `gds-server` validates its flags and exits;
  it does not serve yet.
- The Go client `pkg/client` (failover, typed calls, watch loop, pool and
  router caches, metrics) and the in-memory test server `pkg/fakegds`.

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
- `internal/store/storetest/`: conformance suite every `Store` must pass.
- `internal/registry/`: lease evaluation, TTL bounds, generations, roles and
  pool state derivation.
- `internal/config/`: `gds-server` flags with `GDS_` environment fallbacks.
- `internal/version/`: build metadata set by Bazel `x_defs`.
- `cmd/gds-server/`: server entrypoint (configuration only for now).
- `pkg/client/`: the Go client library; usage, failover and cache semantics
  are in `pkg/client/README.md`.
- `pkg/fakegds/`: in-memory implementation of the five GDS services for tests
  of clients, the controller and integrations.
- `docs/api.md`: REST paths, JSON and stream shapes, errors and limits.
- `hack/proto.sh`: proto lint and code generation.

## Make targets

Run from this directory. The Makefile sets `GOWORK=off`.

```bash
make build         # go build ./... and _output/bin/gds-server
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
the proto sources for vendored copies.

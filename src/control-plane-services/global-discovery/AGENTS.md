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

# AGENTS.md - global-discovery

Global Discovery Service (GDS): the multi-region pool, router and
Multi-Cluster Services registry of the Dynamo Global Router. Read
`README.md` for the layout, flags and operations, `docs/api.md` for the API
and `docs/consistency.md` for the view, write-through and lease model. The
design is `docs-gds/global-discovery-service-design.md` at the repository
root.

## Build and test

From this directory (the Makefile sets `GOWORK=off`):

```bash
make build
make test          # -race, coverage of internal/ must stay at or above 85 percent
make test-integration  # DynamoDB store and gds-admin against dynamodb-local; needs Docker
make vet
make lint
make proto         # after editing api/proto
make proto-check   # before committing
make envtest-assets && make test-envtest   # gds-mcs-controller against kube-apiserver and etcd
make codegen-update  # after changing +kubebuilder:rbac markers under internal/mcs
GOWORK=off go run ./cmd/gds-server --log-level=debug   # memory store on :9090, :8080, :9091
```

From the repository root:

```bash
bazel test //src/control-plane-services/global-discovery/... --test_output=errors
bazel run //:gazelle -- src/control-plane-services/global-discovery
bazel mod tidy     # after adding an external import
```

Run Gazelle through `//:gazelle` with the module path. This module has no
local gazelle target on purpose. Gazelle does not add the license header to
new BUILD files; copy it from an existing one.

## API contract

- Edit `api/proto/nvidia/gds/v1/*.proto`, then run `make proto` and commit the
  regenerated `api/gen`. Never edit `api/gen` by hand.
- Then run `hack/sync-gds-proto.sh` and commit the vendored copy in
  `src/libraries/rust/stargate/crates/gds-client/proto`. Bazel diff tests in
  that crate fail while the copies differ.
- Changes are additive only: new fields, messages, enum values and RPCs.
  Never renumber, rename or remove. A breaking change needs a `v2` package.
- `buf lint` (STANDARD) must pass. Every RPC has its own request and response
  message.
- Keep `docs/api.md` and `api/gateway_test.go` in step with REST paths.
- Label, annotation, reason and limit values in `api/` are a wire contract
  with the Rust gds-client crate and the Helm charts. Change both sides
  together.

## Server code

- Handlers in `internal/api` validate, write to the store, then apply the
  stored record to the view (`view.Apply*`) before responding. Reads come
  from the view only. Return errors through the helpers in
  `internal/api/errors.go` so every error has an ErrorInfo.
- Derived objects in `internal/view` are immutable once published. Never
  modify a message returned by the view; clone it first. When a proto
  message gains a field, update `liteImport` or `litePool` (a test fails
  otherwise).
- Merge rules live in `internal/registry/merge` with the ported Cilium
  test cases; keep them in step with `sigs.k8s.io/mcs-api` reasons.
- Time comes from `internal/clock`; tests use `clock.NewFake` and
  `BlockUntil` before advancing.
- A new store backend registers its opener in `internal/storefactory` and
  its admin commands with `registerCommand` in a new file of `cmd/gds-admin`.

## Store

- Every `Store` implementation must pass `storetest.Run`. Add a conformance
  case for any new store behaviour instead of testing one implementation.
- Store record types mirror the proto messages but use Kubernetes spellings
  for enums. `internal/convert` maps them.
- Writes are conditional; see the package comment of `internal/store`.
- DynamoDB store: `docs/storage.md` is the item catalogue and condition
  table. Change it together with `keys.go`, `codec.go` and `store.go`.
  Table definition changes go to `schema.go`,
  `internal/store/dynamodb/testdata/create-table.json` and
  `docs/storage.md` together.
- The unit tests run the DynamoDB store against an in-memory fake of the
  DynamoDB API (`fake_test.go`). It rejects unused expression placeholders,
  as DynamoDB does, but it does not prove DynamoDB semantics: run
  `make test-integration` after any change to a request or condition.
- Integration tests use the build tag `integration`. Gazelle leaves those
  files out, so their Bazel `go_test` targets are kept by hand (`# keep`).

## Client and fake server

- `pkg/client` is a public API imported by other modules (the MCS
  controller, Pylon Operator, the LLM API gateway). Keep changes additive and
  keep `pkg/client/README.md` in step.
- `pkg/fakegds` is the test server other workstreams import. When the API
  semantics change (error reasons and details, watch event shape, pool state
  rules), update the fake and its tests in the same change.
- Client tests run against `pkg/fakegds` over real loopback listeners and
  bufconn with the race detector: `go test -race ./pkg/...`. The coverage
  gate of `make test` covers `internal/` only; keep `pkg/` tests thorough
  anyway.

## MCS controller

`cmd/gds-mcs-controller` and `internal/mcs/*`; behaviour, flags, RBAC and
metrics are in `docs/mcs-controller.md`. Keep that page in step with the
code.

- Run `make test-envtest` after changing the export or import logic. The
  controller-runtime fake client differs from an API server in ways that
  matter here: it fills apply configurations with the answer of `Apply`
  (the real client does not), it does not separate status from main
  resource field ownership under server-side apply, and it allocates no
  ClusterIPs or UIDs. Unit tests alone have hidden bugs of that kind.
- The envtest suite (`internal/mcs/integration`) skips without
  `KUBEBUILDER_ASSETS`. Its Bazel target is tagged `external` and
  `requires-kubebuilder` and inherits the variable; those attributes are
  kept by hand (`# keep`).
- `config/rbac/role.yaml` is generated: change the `+kubebuilder:rbac`
  markers, run `make codegen-update` and commit the result
  (`make codegen-check` verifies). Leader election RBAC belongs to the chart.
- CRDs are not generated here: ServiceExport and ServiceImport come from
  `sigs.k8s.io/mcs-api` (`config/crd` embeds them), the ClusterProperty CRD
  is kept by hand in `config/crd/bases`.
- Label, annotation, finalizer, field manager and condition values come
  from `api/labels.go` and `api/errors.go`; they are a contract with the
  charts and the Rust crate.
- Everything that writes to GDS or applies imports is leader-only. One
  controller owns all export records of its cluster id: it deletes records
  without a local ServiceExport on every resync.
- Use `internal/mcs/mcstest` (fakegds with a client, a fake Kubernetes
  client with the controller scheme, object builders, metric reads) in
  tests.

## Conventions

- Metrics: `nvcf_gds_*` (server, `internal/metrics`), `nvcf_gds_mcs_controller_*`
  (`internal/mcs/metrics`), `nvcf_gds_client_*`, with pre-initialised
  counters. Add new label values to the initialisation of the package.
- Logs: zap JSON. Do not log and return the same error; the gRPC logging
  interceptor logs every failed call. The controller logs through
  controller-runtime's logr (zap); controller-runtime logs reconcile
  errors.
- Timestamps given to the store are server time; leases are evaluated at
  read time with `registry.LeaseExpired`.
- Flags use the standard `flag` package; every gds-server flag has a `GDS_`
  environment fallback (`internal/config`). The controller's flags
  (`internal/mcs/config`) read only `GDS_URLS` and `POD_NAMESPACE`.

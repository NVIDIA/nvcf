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
# Self-Hosted Idempotency E2E (Manual)

Spec 8.4 of the combined SRD/SDD: the T1-T7 matrix that gates each release.

## Prerequisites

- A host with k3d, helmfile, helm, and a working kubectl context.
- `CONTROL_PLANE_STACK_PATH` env var pointing at a checked-out control-plane stack (`nvcf-self-managed-stack`).
- `COMPUTE_PLANE_STACK_PATH` env var pointing at a checked-out compute-plane stack (`nvcf-compute-plane-stack`).
- `CONTROL_PLANE_STACK_PATH_NEXT` and `COMPUTE_PLANE_STACK_PATH_NEXT` env vars (optional): second stack versions for T5 (version-upgrade). T5 self-skips when unset.
- A valid admin JWT in `$NVCF_TOKEN` env var. Easiest source: `nvcf-cli init` (interactive), then extract the JWT from `~/.nvcf-cli.state`'s `token` field:

```bash
export NVCF_TOKEN=$(jq -r .token ~/.nvcf-cli.state)
```

## Cluster setup

T2/T3/T4 destroy and recreate the test cluster (`ncp-local-e2e`) via `k3d cluster delete + create`.
The recreate command defaults to:

```bash
k3d cluster create ncp-local-e2e --servers 1 --agents 5
```

Override via `E2E_CLUSTER_CREATE_CMD` if your dev VM uses a different cluster shape:

```bash
export E2E_CLUSTER_CREATE_CMD='make -C ../../ncp-local-cluster build-and-deploy-cluster'
```

## Run

```bash
# From the monorepo root
export CONTROL_PLANE_STACK_PATH=/path/to/nvcf-self-managed-stack
export COMPUTE_PLANE_STACK_PATH=/path/to/nvcf-compute-plane-stack
export NVCF_TOKEN=$(jq -r .token ~/.nvcf-cli.state)

# Build the CLI under test
bazel build //src/clis/nvcf-cli:nvcf-cli
cp "$(bazel cquery --output=files //src/clis/nvcf-cli:nvcf-cli)" /tmp/nvcf-cli

# Run the e2e suite (still go-tooling because it shells out to k3d/helm/kubectl)
cd src/clis/nvcf-cli
NVCF_E2E=1 go test -tags=e2e -v ./test/e2e/...
```

`NVCF_E2E=1` is required. The test file panics on startup if this guard is absent.

The e2e tests intentionally remain on `go test -tags=e2e` rather than `bazel
test`: they shell out to `k3d`, `helm`, `helmfile`, `cqlsh`, and `toxiproxy`
(see `make e2e-self-hosted-faults` history), which do not run inside Bazel
sandboxes. Migrating them to Bazel is a separate effort tracked outside the
build-tooling MR that introduced the rest of the Bazel wiring.

## Expected runtime

| Test | Wall time |
|---|---|
| T1 | ~10s (re-run only) |
| T2 | ~10-12 min |
| T3 | ~8 min |
| T4 | ~8 min |
| T5 | ~6 min (or skipped if `CONTROL_PLANE_STACK_PATH_NEXT` and `COMPUTE_PLANE_STACK_PATH_NEXT` are unset) |
| T6 | skipped (orchestrator lock not yet implemented; see test code) |
| T7 | ~2 min |
| Total | ~45-60 min (with T5 and T6 excluded) |

T6 is unconditionally skipped pending the orchestrator-level lock implementation
(`~/.nvcf-cli/state/<cluster>.lock`). Tracked as a post-M6 follow-up.

## Selective runs

```bash
# From src/clis/nvcf-cli (after the build step above)
NVCF_E2E=1 go test -tags=e2e -v -run TestT1 ./test/e2e/...           # Just T1
NVCF_E2E=1 go test -tags=e2e -v -run 'TestT[123]' ./test/e2e/...     # T1-T3
```

## Release gate

Per REQ-4.6: a release is blocked if any of T1-T7 fail. Run before tagging
each `nvcf-cli` release.

## Failure debugging

- Each test logs the full stdout/stderr of every `nvcf` invocation. On failure,
  grep the log for the relevant phase (`>>> Installing control plane`,
  `>>> Cluster registered`, etc.).
- For T2/T3/T4, a partial-state cluster is left running after the interrupted
  first invocation so you can introspect manually via `kubectl`. The second
  invocation (convergence run) recreates the cluster only if `teardownCluster`
  is called again explicitly.
- T3 timing (90 s SIGINT delay) targets the post-control-plane register window.
  On slower hosts the interrupt may land mid-control-plane;
  adjust the delay or replace with a deterministic readiness poll if T3 is
  flaky on your hardware.
- T4 timing (150 s SIGINT delay) targets the post-control-plane+register window.
  Same guidance applies.

## Split-cluster tests (M+9.H - T8, T9, T10)

These tests require two or three k3d clusters and exercise the split-cluster
context routing in `up` / `status`. These tests are not run in CI.

```sh
# After bazel build //src/clis/nvcf-cli:nvcf-cli (see Run section).
cd src/clis/nvcf-cli
NVCF_E2E=1 go test -tags=e2e -v -timeout=60m -run 'TestE2E_T(8|9|10)' ./test/e2e/...
```

Prerequisites:
- `k3d` on PATH
- ~2 GB free RAM per cluster
- Network: clusters need to reach each other (default k3d setup is fine on the same host)

T8: split clean re-run (idempotent). T9: second compute plane against the same
control plane. T10: split interrupted register recovers (kill after phase 6).

The tests currently `t.Skip` with TODO markers pointing at the dev-VM run.
Full integration wires in M+9.I when the dev-VM smoke matrix runs.

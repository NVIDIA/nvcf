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
- `STACK_PATH` env var pointing at a checked-out `nvcf-self-managed-stack`.
- `STACK_PATH_NEXT` env var (optional): second stack version for T5 (version-upgrade). T5 self-skips when unset.
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
cd nvcf-cli
export STACK_PATH=/path/to/nvcf-self-managed-stack
export NVCF_TOKEN=$(jq -r .token ~/.nvcf-cli.state)
make e2e-self-hosted
```

The `make e2e-self-hosted` target sets `NVCF_E2E=1`, which is required. The test file panics
on startup if this guard is absent.

## Expected runtime

| Test | Wall time |
|---|---|
| T1 | ~10s (re-run only) |
| T2 | ~10-12 min |
| T3 | ~8 min |
| T4 | ~8 min |
| T5 | ~6 min (or skipped if `STACK_PATH_NEXT` is unset) |
| T6 | skipped (orchestrator lock not yet implemented; see test code) |
| T7 | ~2 min |
| Total | ~45-60 min (with T5 and T6 excluded) |

T6 is unconditionally skipped pending the orchestrator-level lock implementation
(`~/.nvcf-cli/state/<cluster>.lock`). Tracked as a post-M6 follow-up.

## Selective runs

```bash
make e2e-self-hosted ARGS="-run TestT1"          # Just T1
make e2e-self-hosted ARGS="-run 'TestT[123]'"    # T1-T3
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
NVCF_E2E=1 make e2e-self-hosted-split
```

Prerequisites:
- `k3d` on PATH
- ~2 GB free RAM per cluster
- Network: clusters need to reach each other (default k3d setup is fine on the same host)

T8: split clean re-run (idempotent). T9: second compute plane against the same
control plane. T10: split interrupted register recovers (kill after phase 6).

The tests currently `t.Skip` with TODO markers pointing at the dev-VM run.
Full integration wires in M+9.I when the dev-VM smoke matrix runs.

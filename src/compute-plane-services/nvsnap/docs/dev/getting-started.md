<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Getting started (contributors)

The full loop for landing a change in nvsnap: **build → deploy → capture a
workload → validate → merge.** New here? Read this top to bottom once; after
that the individual sections stand alone.

For install/operations (not building from source), see
[docs/INSTALL.md](../INSTALL.md). For the "why" behind the design, see the
[architecture docs](../../README.md#architecture).

## Prerequisites

- **Build host**: Go 1.24+, Docker (with `DOCKER_HOST` *unset* — never `sudo`),
  `make`, `git`. C toolchain for `lib/nvsnap_intercept`.
- **A GPU cluster to validate on**: Kubernetes 1.25+, NVIDIA driver **555+**
  (required by `cuda-checkpoint`), containerd 1.7+/2.x or CRI-O, `kubectl` +
  `helm` 3.13+. A CPU-only cluster runs the unit tests but not the e2e.

nvsnap depends on forked CRIU, go-criu, and a few patched libraries. Prebuilt
images are the fast path; only changes to the C library or CRIU itself need the
forks built from source. See [CONTRIBUTING.md](../../CONTRIBUTING.md) and
[docs/THIRD-PARTY-FORKS.md](../THIRD-PARTY-FORKS.md).

## 1. Build

```bash
make build          # agent + restore-entrypoint + server + blobstore binaries
make test           # Go unit tests (no GPU needed)
```

Container images are built by `scripts/build-agent.sh` (agent base + app) and
`ci/build-image.sh` (every shipped image, tagged from `scripts/versions.sh`).
The `base` image carries forked CRIU and rarely changes; the `app` image is the
Go binaries + `libnvsnap_intercept.so` and is what you rebuild most often:

```bash
./scripts/build-agent.sh app && ./scripts/build-agent.sh push-app
```

Bump the image tag on every rebuild — Kubernetes caches by tag
(`IfNotPresent`), so reusing a tag ships stale code. See `CLAUDE.md` rule 19.

## 2. Deploy to a cluster

```bash
./scripts/install-nvsnap.sh                    # agent + server + blobstore (+ webhook)
kubectl -n nvsnap-system rollout status ds/nvsnap-agent
```

`install-nvsnap.sh` wraps namespace + pull-secret + `helm install`
([deploy/helm/nvsnap](../../deploy/helm/nvsnap)). Use `--without-webhook` to
skip cert-manager + the admission webhook. Details:
[docs/PULL-SECRET-SETUP.md](../PULL-SECRET-SETUP.md).

## 3. Capture a workload (the capture spec)

nvsnap picks the capture path automatically per workload; you don't choose it
in code, you *label* the workload:

- **Single-GPU** → CRIU + `cuda-checkpoint` snapshot the whole process. Trigger
  it via the agent/server API (`POST /api/v1/checkpoints`, see the
  [example client](../../examples/checkpoint-restore)).
- **Multi-GPU / large models / cachedir mode** → the rootfs/cachedir path. Add
  the label `nvsnap.io/capture: "true"` to the workload pod. At admission the
  webhook injects the capture plumbing (`/opt/nvsnap` for cachedir), and once
  the pod is Ready + warm the agent's `rootfsonly.Watcher` captures it into a
  **capture manifest** — a `ConfigMap` labeled
  `nvsnap.io/kind=rootfs-capture-manifest` whose `manifest.json` records the
  hash, capture method, cache dir, source pod metadata, and volumes. That
  manifest *is* the portable capture spec; restore replays it on any node via a
  `nvsnap.io/restore-from: <hash>` annotation.

The label must be present **at pod creation** — the webhook only injects the
capture volume for pods that carry it when admitted.

## 4. Validate

Every change that touches `lib/nvsnap_intercept/`, `cmd/restore-entrypoint/`,
the patched libraries, or K8s manifests **must** pass an end-to-end run on a
real GPU cluster before merge (`CLAUDE.md` rule 10):

```bash
./scripts/test-e2e.sh vllm-small     # single-GPU: deploy → warm → capture → restore → verify inference
CAPTURE_PATH=rootfs ./scripts/test-e2e.sh vllm-small   # force the rootfs/cachedir path
```

`scripts/test-bench.sh <workload>` runs the same flow with cold/capture/restore
timings for the benchmark matrix. Both verify inference *after* restore — a run
only passes if the restored engine actually serves.

## 5. Open a merge request

1. Branch from `main` (never commit straight to `main`).
2. Bump any rebuilt image tag (rule 19).
3. Push; the GitLab pipeline runs lint, `bazel build/test`, and the image
   builds. Fix red before requesting review.
4. Attach the e2e result for changes under the paths in step 4.

Issues and MRs live on GitLab (`nvcf/nvcf-cache/nvsnap`). Reference the issue
number in the MR. See [CONTRIBUTING.md](../../CONTRIBUTING.md) for the full
policy.

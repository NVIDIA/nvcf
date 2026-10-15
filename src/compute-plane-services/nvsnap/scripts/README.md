<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# scripts/

Build, deploy, and test automation. Prefer these over ad-hoc `kubectl`/`docker`
commands — they encode setup that's easy to get wrong by hand. Key entry points
(grouped by purpose):

## Versions
- [`versions.sh`](versions.sh) — **single source of truth** for image tags,
  registry, and fork repos/refs. Sourced by every build/deploy script.
- [`sync-versions.sh`](sync-versions.sh) — stamp the current tags into the K8s
  manifests.

## Build
- [`build-agent.sh`](build-agent.sh) — agent base/app images (`base`, `app`,
  `push-*`, `deploy`).
- [`build-deps.sh`](build-deps.sh), `build-libzmq-image.sh`,
  `build-uvloop-wheel.sh`, `build-pyzmq-wheel.sh` — patched dependency images.
- CI builds every image via [`../ci/build-image.sh`](../ci/build-image.sh).

## Deploy
- [`install-nvsnap.sh`](install-nvsnap.sh) — one-command cluster bootstrap
  (namespace + pull secret + helm; `--without-webhook` to skip cert-manager).

## Test / validate
- [`test-e2e.sh`](test-e2e.sh) `<workload>` — deploy → warm → capture → restore →
  verify inference. The merge gate for capture/restore changes (`CLAUDE.md`
  rule 10). `CAPTURE_PATH=rootfs` forces the rootfs/cachedir path.
- [`test-bench.sh`](test-bench.sh) `<workload>` — same flow with cold/capture/
  restore timings for the benchmark matrix.
- [`checkpoint.sh`](checkpoint.sh) — checkpoint-creation helper (exit 42 on a
  rootfs redirect).

## Rules

Scripts live on disk and are version-controlled — never type build commands
ad-hoc in a terminal (`CLAUDE.md` rule 13). Keep `DOCKER_HOST` unset; never
`sudo` docker.

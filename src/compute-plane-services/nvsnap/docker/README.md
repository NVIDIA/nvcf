<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# docker/

Dockerfiles for every shipped image. Built by [`ci/build-image.sh`](../ci/build-image.sh)
(and `scripts/build-agent.sh` for the agent), tagged from
[`scripts/versions.sh`](../scripts/versions.sh).

## Image map

**Agent (two-stage: heavy base + fast app)**
- [`agent/Dockerfile.base`](agent/) — CRIU + system deps; changes rarely.
- [`agent/Dockerfile.app`](agent/) — Go binaries + `libnvsnap_intercept.so`,
  layered on the base; the one you rebuild most.
- [`init/`](init/) — combined init image (patched deps + agent bundle).

**Services**
- [`Dockerfile.server`](Dockerfile.server), [`nvsnap-blobstore/`](nvsnap-blobstore/),
  [`nvsnap-l2-wait/`](nvsnap-l2-wait/).

**Dependency builders** (patched forks; see
[docs/THIRD-PARTY-FORKS.md](../docs/THIRD-PARTY-FORKS.md))
- [`uvloop/`](uvloop/), [`libzmq/`](libzmq/), [`libuv/`](libuv/),
  [`pyzmq/`](pyzmq/), [`criu-builder/`](criu-builder/).

## Rules

- Every image has a checked-in Dockerfile — never build from an ad-hoc inline
  Dockerfile (`CLAUDE.md` rule 13).
- Bump the image tag in `versions.sh` on every rebuild; nodes cache by tag
  (rule 19).
- Dependency builders must smoke-test the produced `.so`/wheel loads before push.

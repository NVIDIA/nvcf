<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Third-Party Forks: Why We Patch What We Patch

nvsnap ships one patched third-party component: CRIU. Everything else in
the agent image is stock (cuda-checkpoint is built from NVIDIA's public
driver checkpoint API, see CONTRIBUTING.md). This document records what
the CRIU fork carries, why, and how it is built and tracked.

## Summary at a glance

| Component | Upstream | What we patch | Why |
|---|---|---|---|
| `criu` | github.com/checkpoint-restore/criu `criu-dev` | io_uring dump/restore, portable ghost files, CUDA plugin behaviour, epoll tolerance, failure propagation | GPU inference engines (vLLM, SGLang, NIM) restore with stock event loops and stock NCCL |

Retired (no longer built or shipped): the patched `libuv`, `uvloop`,
`libzmq` and `pyzmq` forks, the `go-criu` fork, `libnvsnap_intercept.so`
and the in-pod `restore-entrypoint`. The in-namespace engine (criu-v2)
restores io_uring rings and epoll state inside CRIU, so no userspace
interception is injected into workloads any more. Their history lives in
the `archive/*` tags of the old fork repository.

## criu

Fork of record: `https://github.com/balajinvda/criu`, branch `criu-dev`.
The base image is built from the commit `scripts/versions.sh` pins in
`NVSNAP_CRIU_REF` (tagged `nvsnap-base-<base version>` in the fork), and
`scripts/build-agent.sh base` refuses any other source. Tracking is by
periodic merge of upstream `criu-dev`; the delta is kept to the features
below so the next merge stays small.

Features the fork carries on top of upstream:

1. io_uring checkpoint and restore. Quiesced rings are dumped and
   restored, including SQPOLL rings, worker threads and the SQ-array
   identity map. Stock libuv and uvloop restore without changes, which is
   what made the patched event-loop libraries unnecessary.
2. Opt-in `--ghost-links` for portable images. Unlinked-but-open files
   (Python multiprocessing semaphores under /dev/shm, engine temp files)
   are carried as ghost entries that restore on any node.
3. CUDA plugin behaviour: character-device fd and VMA claims for the
   per-GPU nvidia device nodes, restore and unlock of every process in one
   `cuda-checkpoint` spawn with feature-detected resume, and a late device
   resume failure that fails the restore instead of being logged at debug
   level and leaving workers checkpointed.
4. Tolerance for an epoll fd shared across fork-inherited processes
   (engine worker trees share the parent's epoll instance).

Upstream renamed `--compress-region` to `--compress-block` and split the
CUDA plugin into CLI and Driver API backends; the fork follows upstream
there and the agent probes the bundled binary for the flag spelling.

How nvsnap uses it: the agent stages the bundle into the workload's
mount namespace and runs `criu dump` and `criu restore` there (criu-v2).
The CUDA plugin drives cuda-checkpoint during the dump and restore, so
GPU state needs no separate orchestration.

## Build provenance

`scripts/build-agent.sh base` clones the fork at `NVSNAP_CRIU_REF` (or
uses `NVSNAP_CRIU_SRC` when set) and fails if the source HEAD is not the
pinned commit. The resulting image records the commit in
`/criu-bundle/criu-version`. `scripts/build-agent.sh check-criu-ref`
reports drift between the pin and a local checkout.

## Upstream contribution status

Candidates, in order of readiness:

- io_uring checkpoint and restore: self-contained, has unit tests, and is
  the feature upstream users ask for most. Needs the SQPOLL and
  multi-ring cases split into reviewable patches.
- Late device restore failure propagation: small and uncontroversial.
- Portable ghost links: needs an upstream discussion about image format
  compatibility.

The epoll shared-fd tolerance is specific to how inference engines fork
and is kept local. Until upstream picks these up, the fork is long lived
and rebased onto upstream `criu-dev` at each base image bump.

<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# cmd/

Entry points (`main` packages). One directory per binary.

## Binaries

- [`agent/`](agent/) — **nvsnap-agent**, the node DaemonSet: process
  discovery, checkpoint/restore orchestration, the rootfs capture watcher, the
  mutating webhook, and the peer-cascade HTTP server.
- [`nvsnap-server/`](nvsnap-server/) — REST API, web UI, and checkpoint catalog.
- [`nvsnap/`](nvsnap/) — the `nvsnap` CLI.
- [`nvsnap-blobstore/`](nvsnap-blobstore/) — disk-backed content-addressed blob
  store (the L3 tier).
- [`restore-entrypoint/`](restore-entrypoint/) — CRIU-path restore entrypoint:
  waits for the dump, runs CRIU restore + cuda-checkpoint replay, reinitializes
  io_uring/libuv/zmq.
- [`nvsnap-rootfs-restore/`](nvsnap-rootfs-restore/) — rootfs/cachedir restore
  shim: mounts the captured cache and prewarms it (no in-process restore).
- [`nvsnap-mount-prep/`](nvsnap-mount-prep/) — init container the webhook
  injects to stage restore mounts.
- [`nvsnap-l2-wait/`](nvsnap-l2-wait/) — init container that blocks until the L2
  per-capture PVC is promoted.
- [`nvsnap-gpu-restore/`](nvsnap-gpu-restore/) — GPU restore helper (C) invoking
  the CUDA Checkpoint API.

## Rules

Keep `main` thin — wiring and flag parsing only. Logic lives in
[`../internal/`](../internal/) (private) or [`../pkg/`](../pkg/) (importable).

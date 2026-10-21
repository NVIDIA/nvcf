<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# cmd/

Entry points (`main` packages). One directory per binary.

## Binaries

- [`agent/`](agent/) — **nvsnap-agent**, the node DaemonSet: process
  discovery, checkpoint/restore orchestration, the cachedir capture watcher, the
  mutating webhook, and the peer-cascade HTTP server.
- [`nvsnap-server/`](nvsnap-server/) — REST API, web UI, and checkpoint catalog.
- [`nvsnap/`](nvsnap/) — the `nvsnap` CLI.
- [`nvsnap-rootfs-restore/`](nvsnap-rootfs-restore/) — rootfs/cachedir restore
  shim: mounts the captured cache and prewarms it (no in-process restore).
- [`nvsnap-mount-prep/`](nvsnap-mount-prep/) — init container the webhook
  injects to stage restore mounts.
- [`nvsnap-l2-wait/`](nvsnap-l2-wait/) — init container that blocks until the L2
  per-capture PVC is promoted.
- [`nvsnap-helper/`](nvsnap-helper/): static helper that runs the steps the
  webhook adds to a pod (wait, write, seed, prewarm, idle) in any image.
- [`nvsnap-idle/`](nvsnap-idle/): static C program that holds a CRIU restore
  placeholder's container until the agent restores the captured processes.

## Rules

Keep `main` thin — wiring and flag parsing only. Logic lives in
[`../internal/`](../internal/) (private) or [`../pkg/`](../pkg/) (importable).
